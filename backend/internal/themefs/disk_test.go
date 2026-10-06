package themefs

import (
	"bytes"
	"context"
	"io"
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
	// "hello" base64-encoded — proves raw bytes round-trip intact through ReadFileBytes, unlike ReadFile's string conversion.
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

// An image upload must reach FlowPOS as raw multipart bytes, never through the text write's JSON body.
func TestStore_UploadFile_SendsRawBytesAsMultipart(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0x00, 0x01, 0x80, 0xFE}
	var gotPath, gotAuth, gotTID, gotName, gotType string
	var gotData []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotTID = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("TID")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("expected a multipart \"file\" field: %v", err)
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		gotData, _ = io.ReadAll(file)
		gotName, gotType = header.Filename, header.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	err := NewStore(ts.URL).UploadFile(context.Background(), RequestAuth{Token: "tok", TenantID: 7}, "images/hero.jpg", data, "image/jpeg")
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if gotPath != "/store/themes/active/files/images/hero.jpg" || gotAuth != "Bearer tok" || gotTID != "7" {
		t.Errorf("request = %s auth=%q tid=%q", gotPath, gotAuth, gotTID)
	}
	if !bytes.Equal(gotData, data) || gotName != "hero.jpg" || gotType != "image/jpeg" {
		t.Errorf("uploaded %v as %q (%q), want the exact bytes as hero.jpg (image/jpeg)", gotData, gotName, gotType)
	}
}

func TestStore_UploadFile_Errors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer ts.Close()
	store := NewStore(ts.URL)

	tests := []struct {
		name string
		path string
	}{
		{"rejected by FlowPOS", "images/hero.jpg"},
		{"unsafe path never sent", "../secret.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.UploadFile(context.Background(), RequestAuth{}, tt.path, []byte{1}, "image/jpeg"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
