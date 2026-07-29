package themefs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeManifestServer serves a fixed theme and counts how many times each
// endpoint was hit, so tests can assert a cache hit skips the expensive
// per-file reads.
type fakeManifestServer struct {
	files     map[string]string
	listCalls atomic.Int64
	readCalls atomic.Int64
	ts        *httptest.Server
}

func newFakeManifestServer(files map[string]string) *fakeManifestServer {
	f := &fakeManifestServer{files: files}
	f.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			f.listCalls.Add(1)
			entries := make([]FileTreeEntry, 0, len(f.files))
			for p := range f.files {
				entries = append(entries, FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}})
			return
		}
		f.readCalls.Add(1)
		path := r.URL.Path[len("/store/themes/active/files/"):]
		content, ok := f.files[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"path": path, "content": content, "encoding": "utf-8"}})
	}))
	return f
}

func TestGenerateManifest_InfersComponentParams(t *testing.T) {
	files := map[string]string{
		"components/testimonials.liquid": `{% if show_title %}<h2>{{ title }}</h2>{% endif %}
{% for t in reviews %}<p>{{ t.name }}: {{ t.quote }}</p>{% endfor %}
{{ theme.asset_base }}`,
		"pages/home.liquid": `{{ store.name }}`,
	}
	server := newFakeManifestServer(files)
	defer server.ts.Close()
	store := NewStore(server.ts.URL)

	m, err := store.GenerateManifest(context.Background(), RequestAuth{Token: "t", TenantID: 1})
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}
	if len(m.Files) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(m.Files), m.Files)
	}
	if len(m.Components) != 1 || m.Components[0].Path != "components/testimonials.liquid" {
		t.Fatalf("expected exactly components/testimonials.liquid indexed, got %+v", m.Components)
	}

	params := m.Components[0].Params
	want := map[string]bool{"show_title": true, "title": true, "reviews": true}
	if len(params) != len(want) {
		t.Fatalf("expected params %v, got %v", want, params)
	}
	for _, p := range params {
		if !want[p] {
			t.Errorf("unexpected inferred param %q (theme.asset_base's root and the loop var 't' must be excluded)", p)
		}
	}
	if m.ContentHash == "" {
		t.Error("expected a non-empty content hash")
	}
}

func TestGetOrGenerateManifest_SecondCallUsesCacheWhenUnchanged(t *testing.T) {
	files := map[string]string{"components/hero.liquid": "{{ headline }}"}
	server := newFakeManifestServer(files)
	defer server.ts.Close()
	store := NewStore(server.ts.URL)
	auth := RequestAuth{Token: "t", TenantID: 1}

	first, err := store.GetOrGenerateManifest(context.Background(), auth)
	if err != nil {
		t.Fatalf("first GetOrGenerateManifest failed: %v", err)
	}
	readsAfterFirst := server.readCalls.Load()
	if readsAfterFirst == 0 {
		t.Fatal("expected the first call to actually read files")
	}

	second, err := store.GetOrGenerateManifest(context.Background(), auth)
	if err != nil {
		t.Fatalf("second GetOrGenerateManifest failed: %v", err)
	}

	if server.readCalls.Load() != readsAfterFirst {
		t.Errorf("expected no additional file reads on a cache hit, went from %d to %d", readsAfterFirst, server.readCalls.Load())
	}
	if second.GeneratedAt != first.GeneratedAt {
		t.Error("expected the cached manifest's GeneratedAt to be reused unchanged, not regenerated")
	}
	if second.ContentHash != first.ContentHash {
		t.Error("expected the same content hash from cache")
	}
}

func TestGetOrGenerateManifest_RegeneratesWhenFileSetChanges(t *testing.T) {
	files := map[string]string{"components/hero.liquid": "{{ headline }}"}
	server := newFakeManifestServer(files)
	defer server.ts.Close()
	store := NewStore(server.ts.URL)
	auth := RequestAuth{Token: "t", TenantID: 1}

	first, err := store.GetOrGenerateManifest(context.Background(), auth)
	if err != nil {
		t.Fatalf("first GetOrGenerateManifest failed: %v", err)
	}

	server.files["components/new-one.liquid"] = "{{ subtitle }}"
	second, err := store.GetOrGenerateManifest(context.Background(), auth)
	if err != nil {
		t.Fatalf("second GetOrGenerateManifest failed: %v", err)
	}

	if second.ContentHash == first.ContentHash {
		t.Error("expected a different content hash after the file set changed")
	}
	if len(second.Components) != 2 {
		t.Errorf("expected 2 components after the change, got %d", len(second.Components))
	}
}

func TestGetOrGenerateManifest_CachePerTenant(t *testing.T) {
	files := map[string]string{"components/hero.liquid": "{{ headline }}"}
	server := newFakeManifestServer(files)
	defer server.ts.Close()
	store := NewStore(server.ts.URL)

	if _, err := store.GetOrGenerateManifest(context.Background(), RequestAuth{Token: "t", TenantID: 1}); err != nil {
		t.Fatalf("tenant 1 GetOrGenerateManifest failed: %v", err)
	}
	readsAfterTenant1 := server.readCalls.Load()

	if _, err := store.GetOrGenerateManifest(context.Background(), RequestAuth{Token: "t", TenantID: 2}); err != nil {
		t.Fatalf("tenant 2 GetOrGenerateManifest failed: %v", err)
	}
	if server.readCalls.Load() == readsAfterTenant1 {
		t.Error("expected a different tenant's first call to still do real work, not reuse tenant 1's cache entry")
	}
}
