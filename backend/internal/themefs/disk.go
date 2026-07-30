package themefs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Store reads and writes the tenant's active theme's files through
// flowpos-backend's own theme-file API (store/themes/active/files/...) —
// see ThemeFileController/ThemeFileService in flowpos-backend. This service
// never touches theme files on local disk: it doesn't assume it shares a
// filesystem mount with flowpos-backend, so the two can run on separate
// hosts. Every call authenticates as the same user who made the original
// request (see RequestAuth) — flowpos-backend applies its own ownership
// checks (ActorGate) on every file access, the same as it would for a
// request from the dashboard's own Editor page.
type Store struct {
	baseURL string
	client  *http.Client
	// manifests caches GetOrGenerateManifest's results — see manifest.go.
	manifests manifestCache
}

// NewStore builds a Store calling baseURL (config.FlowposAPIBase) — the same
// root internal/auth.Client calls /user against.
func NewStore(baseURL string) *Store {
	return &Store{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{},
	}
}

// RequestAuth is the caller identity Store forwards on every call — the
// same bearer token and resolved tenant ID this service's own auth
// middleware already validated for the current request (see auth.Token,
// auth.TenantID). Token authenticates as that user; TenantID is sent as the
// TID header flowpos-backend's own SetActorMiddleware requires to select
// which of that user's tenants the request acts as — the same purpose this
// service's own X-Tenant-Id header serves, just a different header name on
// flowpos-backend's side.
type RequestAuth struct {
	Token    string
	TenantID uint64
}

// PageMeta is the subset of pages.json fields ai-chat can set when writing
// a page file — flowpos-backend's own store() endpoint upserts pages.json
// itself from these (see ThemeFileService::save / ThemePagesManifest)
// whenever the path matches pages/*.liquid, so this service no longer
// merges pages.json itself. published_at is deliberately not sent:
// flowpos-backend stamps it itself when Status is "published" and it's
// omitted, which is the same "just-published" semantics this service used
// to set explicitly. requires_auth is a known gap: flowpos-backend's store()
// endpoint doesn't currently forward it from this API (only title/slug/
// type/status/seo_*/og_* — see ThemeFileController::store), so a page the
// model wants gated can't be expressed through this path yet.
type PageMeta struct {
	Title          string `json:"title,omitempty"`
	Slug           string `json:"slug,omitempty"`
	Type           string `json:"type,omitempty"`
	Status         string `json:"status,omitempty"`
	SEOTitle       string `json:"seo_title,omitempty"`
	SEODescription string `json:"seo_description,omitempty"`
	SEOKeywords    string `json:"seo_keywords,omitempty"`
	OGTitle        string `json:"og_title,omitempty"`
	OGDescription  string `json:"og_description,omitempty"`
	OGImagePath    string `json:"og_image_path,omitempty"`
}

type themeFileEnvelope struct {
	Data struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	} `json:"data"`
}

// ReadFile returns a theme file's current content, or ("", nil) if it
// doesn't exist yet (e.g. reading pages.json for a theme with no custom
// pages registered yet is a normal, empty case, not an error).
func (s *Store) ReadFile(ctx context.Context, auth RequestAuth, relPath string) (string, error) {
	if err := ValidatePathSafety(relPath); err != nil {
		return "", err
	}

	req, err := s.newRequest(ctx, auth, http.MethodGet, relPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", relPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read %s: %s", relPath, statusErr(resp))
	}

	var out themeFileEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("read %s: decode response: %w", relPath, err)
	}
	if out.Data.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(out.Data.Content)
		if err != nil {
			return "", fmt.Errorf("read %s: decode base64 content: %w", relPath, err)
		}
		return string(decoded), nil
	}
	return out.Data.Content, nil
}

// WriteFile upserts a theme file's content. meta is only meaningful for a
// pages/*.liquid path — see PageMeta — and nil otherwise.
func (s *Store) WriteFile(ctx context.Context, auth RequestAuth, relPath, content string, meta *PageMeta) error {
	if err := ValidatePathSafety(relPath); err != nil {
		return err
	}

	body := map[string]any{"content": content}
	if meta != nil {
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("write %s: encode page meta: %w", relPath, err)
		}
		if err := json.Unmarshal(metaJSON, &body); err != nil {
			return fmt.Errorf("write %s: encode page meta: %w", relPath, err)
		}
		body["content"] = content
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("write %s: encode request: %w", relPath, err)
	}

	req, err := s.newRequest(ctx, auth, http.MethodPost, relPath, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("write %s: %s", relPath, statusErr(resp))
	}
	return nil
}

// DeleteFile removes a theme file — used by revert (see themebuild's
// RevertToMessage) to undo a file that didn't exist yet at the point being
// reverted to. A no-op (not an error) if the file is already gone.
// flowpos-backend's own delete endpoint un-registers the pages.json entry
// too when relPath is a pages/*.liquid file (see ThemeFileService::delete),
// so a reverted new page is fully removed, not just its file.
func (s *Store) DeleteFile(ctx context.Context, auth RequestAuth, relPath string) error {
	if err := ValidatePathSafety(relPath); err != nil {
		return err
	}

	req, err := s.newRequest(ctx, auth, http.MethodDelete, relPath, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s: %w", relPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete %s: %s", relPath, statusErr(resp))
	}
	return nil
}

// FileTreeEntry is one node of the theme's file tree, as returned by
// flowpos-backend's GET store/themes/active/files
// (ThemeFileController::index -> ThemeFileService::listTree) — a directory
// carries Children (recursively, the whole subtree), a file doesn't.
type FileTreeEntry struct {
	Name     string          `json:"name"`
	Path     string          `json:"path"`
	Type     string          `json:"type"` // "directory" | "file"
	Children []FileTreeEntry `json:"children,omitempty"`
}

type fileTreeEnvelope struct {
	Data struct {
		Files []FileTreeEntry `json:"files"`
	} `json:"data"`
}

// ListFiles returns the active theme's full file tree (names and paths
// only, no content) — the same data flowpos-backend's dashboard Editor
// sidebar renders from. One shared method: themebuild's Check() snapshot
// (phase 1) and the AI tool loop's list_theme_files tool (phase 2) both call
// this rather than each hitting the endpoint their own way.
func (s *Store) ListFiles(ctx context.Context, auth RequestAuth) ([]FileTreeEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/store/themes/active/files", nil)
	if err != nil {
		return nil, fmt.Errorf("list files: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("TID", strconv.FormatUint(auth.TenantID, 10))
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list files: %s", statusErr(resp))
	}

	var out fileTreeEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("list files: decode response: %w", err)
	}
	return out.Data.Files, nil
}

type createThemeEnvelope struct {
	Data struct {
		Theme struct {
			Slug string `json:"slug"`
		} `json:"theme"`
	} `json:"data"`
}

// CreateThemeFromBase creates a brand-new theme for the caller's store from
// flowpos-backend's "AI Base Theme" catalog entry (see
// ThemeController::createFromBase / Database\Seeders\BaseThemeSeeder) and
// returns its generated slug. flowpos-backend assigns the slug itself (it's
// also the on-disk folder name, unique across all stores) — there is no way
// to request a specific one. Used when a merchant starts a theme from
// scratch: ai-chat never scaffolds theme files itself, since it doesn't own
// theme storage (see Store's package doc).
func (s *Store) CreateThemeFromBase(ctx context.Context, auth RequestAuth) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/store/themes/from-base", nil)
	if err != nil {
		return "", fmt.Errorf("create theme from base: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("TID", strconv.FormatUint(auth.TenantID, 10))
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("create theme from base: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create theme from base: %s", statusErr(resp))
	}

	var out createThemeEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("create theme from base: decode response: %w", err)
	}
	if out.Data.Theme.Slug == "" {
		return "", fmt.Errorf("create theme from base: response missing theme slug")
	}
	return out.Data.Theme.Slug, nil
}

func (s *Store) newRequest(ctx context.Context, auth RequestAuth, method, relPath string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+"/store/themes/active/files/"+encodePathSegments(relPath), body)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", relPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("TID", strconv.FormatUint(auth.TenantID, 10))
	return req, nil
}

// encodePathSegments percent-encodes each "/"-separated segment of relPath
// individually, so the literal "/" separators survive in the URL — matches
// the tenant-dashboard's own encodeThemeFilePath (src/store-builder/api/
// editor.ts), which calls this exact same route.
func encodePathSegments(relPath string) string {
	segments := strings.Split(relPath, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

func statusErr(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	return fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, string(body))
}
