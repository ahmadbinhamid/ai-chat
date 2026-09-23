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
	"time"
)

// ThemeStore is the subset of *Store's behavior themebuild depends on, so OverlayStore can wrap either a real *Store or a test fake.
// Deliberately excludes GetOrGenerateManifest — that stays a *Store-only capability, reached via type assertion where needed.
type ThemeStore interface {
	ReadFile(ctx context.Context, auth RequestAuth, relPath string) (string, error)
	WriteFile(ctx context.Context, auth RequestAuth, relPath, content string, meta *PageMeta) error
	DeleteFile(ctx context.Context, auth RequestAuth, relPath string) error
	ListFiles(ctx context.Context, auth RequestAuth) ([]FileTreeEntry, error)
}

// Store reads/writes the active theme's files via flowpos-backend's theme-file API — never touches local disk, so the two
// services can run on separate hosts. Every call authenticates as the requesting user; flowpos-backend applies its own ownership checks.
type Store struct {
	baseURL string
	client  *http.Client
	// manifests caches GetOrGenerateManifest's results — see manifest.go.
	manifests manifestCache
}

// storeHTTPTimeout bounds every file-API call so a hung connection doesn't block indefinitely on the caller's own context.
// Generous, not tight — this client also fetches multi-MB theme assets, not just small JSON payloads.
const storeHTTPTimeout = 60 * time.Second

// NewStore builds a Store calling baseURL (config.FlowposAPIBase) — the same
// root internal/auth.Client calls /user against.
func NewStore(baseURL string) *Store {
	return &Store{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: storeHTTPTimeout},
	}
}

// RequestAuth is the caller identity Store forwards on every call — the same bearer token and tenant ID this service's
// auth middleware already validated. TenantID is sent as the TID header flowpos-backend's SetActorMiddleware requires.
type RequestAuth struct {
	Token    string
	TenantID uint64
}

// PageMeta is the subset of pages.json fields ai-chat can set; flowpos-backend's store() endpoint upserts pages.json from
// these for pages/*.liquid paths. requires_auth is a known gap — flowpos-backend doesn't forward it from this API yet.
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

// ReadFile returns a theme file's current content, or ("", nil) if it doesn't exist yet — a normal case, not an error.
func (s *Store) ReadFile(ctx context.Context, auth RequestAuth, relPath string) (string, error) {
	b, err := s.readFileRaw(ctx, auth, relPath)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReadFileBytes is ReadFile's binary-safe counterpart — converting non-UTF-8 content (images/fonts) through a Go string
// like ReadFile does would silently corrupt it. Not part of ThemeStore; reached via a type assertion where needed.
func (s *Store) ReadFileBytes(ctx context.Context, auth RequestAuth, relPath string) ([]byte, error) {
	return s.readFileRaw(ctx, auth, relPath)
}

// readFileRaw is the shared HTTP round trip + base64 decoding behind ReadFile and ReadFileBytes.
func (s *Store) readFileRaw(ctx context.Context, auth RequestAuth, relPath string) ([]byte, error) {
	if err := ValidatePathSafety(relPath); err != nil {
		return nil, err
	}

	req, err := s.newRequest(ctx, auth, http.MethodGet, relPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.doReadWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read %s: %s", relPath, statusErr(resp))
	}

	var out themeFileEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("read %s: decode response: %w", relPath, err)
	}
	if out.Data.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(out.Data.Content)
		if err != nil {
			return nil, fmt.Errorf("read %s: decode base64 content: %w", relPath, err)
		}
		return decoded, nil
	}
	return []byte(out.Data.Content), nil
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("write %s: %s", relPath, statusErr(resp))
	}
	return nil
}

// DeleteFile removes a theme file; a no-op if already gone. For a pages/*.liquid path, flowpos-backend's delete endpoint
// also un-registers the pages.json entry, so a reverted new page is fully removed, not just its file.
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

// FileTreeEntry is one node of the theme's file tree — a directory carries Children (recursively), a file doesn't.
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

// ListFiles returns the active theme's full file tree (names/paths only, no content) — one shared method both
// themebuild's Check() and the AI tool loop's list_theme_files call, rather than each hitting the endpoint separately.
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

func (s *Store) newRequest(ctx context.Context, auth RequestAuth, method, relPath string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+"/store/themes/active/files/"+encodePathSegments(relPath), body)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", relPath, err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("TID", strconv.FormatUint(auth.TenantID, 10))
	return req, nil
}

// encodePathSegments percent-encodes each "/"-separated segment individually, so literal "/" separators survive in the URL.
func encodePathSegments(relPath string) string {
	segments := strings.Split(relPath, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// readRetryBackoff is how long doReadWithRetry waits between attempts — the
// two delays used across readRetryAttempts-1 retries.
var readRetryBackoff = []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}

// readRetryAttempts bounds ReadFile's retries against a transient upstream failure, so one blip doesn't fail a long
// generation. Not applied to WriteFile — retrying a write that may have landed risks a duplicate/partial write. Must stay len(readRetryBackoff)+1.
var readRetryAttempts = len(readRetryBackoff) + 1

// doReadWithRetry runs req (a GET, safe to resend) and retries on a network error or 5xx; anything else returns immediately.
func (s *Store) doReadWithRetry(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < readRetryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(readRetryBackoff[attempt-1]):
			}
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s", statusErr(resp))
			_ = resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func statusErr(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	return fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, string(body))
}
