package themefs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStore_ListFiles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/store/themes/active/files" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"files": [
					{"name": "pages", "path": "pages", "type": "directory", "children": [
						{"name": "offers.liquid", "path": "pages/offers.liquid", "type": "file"}
					]},
					{"name": "pages.json", "path": "pages.json", "type": "file"}
				]
			},
			"status": true
		}`))
	}))
	defer ts.Close()

	store := NewStore(ts.URL)
	entries, err := store.ListFiles(context.Background(), RequestAuth{Token: "t", TenantID: 1})
	if err != nil {
		t.Fatalf("ListFiles returned an error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 top-level entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Type != "directory" || len(entries[0].Children) != 1 || entries[0].Children[0].Path != "pages/offers.liquid" {
		t.Errorf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].Path != "pages.json" || entries[1].Type != "file" {
		t.Errorf("unexpected second entry: %+v", entries[1])
	}
}

func TestStore_ReadFileBytes_Base64Decodes(t *testing.T) {
	// "hello" base64-encoded — proves the raw bytes round-trip intact
	// through ReadFileBytes, unlike ReadFile's string conversion which
	// would corrupt genuinely binary content (see ReadFileBytes's doc
	// comment on why it exists as a separate method).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"path":"images/hero.png","content":"aGVsbG8=","encoding":"base64"},"status":true}`))
	}))
	defer ts.Close()

	store := NewStore(ts.URL)
	b, err := store.ReadFileBytes(context.Background(), RequestAuth{Token: "t", TenantID: 1}, "images/hero.png")
	if err != nil {
		t.Fatalf("ReadFileBytes returned an error: %v", err)
	}
	if string(b) != "hello" {
		t.Errorf("ReadFileBytes = %q, want %q", string(b), "hello")
	}
}

func TestStore_ReadFileBytes_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	store := NewStore(ts.URL)
	b, err := store.ReadFileBytes(context.Background(), RequestAuth{Token: "t", TenantID: 1}, "images/missing.png")
	if err != nil {
		t.Fatalf("ReadFileBytes returned an error: %v", err)
	}
	if b != nil {
		t.Errorf("ReadFileBytes = %v, want nil for a 404", b)
	}
}

func TestStore_ListFiles_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	store := NewStore(ts.URL)
	if _, err := store.ListFiles(context.Background(), RequestAuth{Token: "t", TenantID: 1}); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}
