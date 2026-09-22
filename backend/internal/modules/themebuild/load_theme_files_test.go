package themebuild

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-chat/internal/themefs"
)

// Returns CSS/JS (when includeAssets) and reads concurrently.
func TestLoadThemeFiles_ReturnsAssetsAndReadsConcurrently(t *testing.T) {
	files := map[string]string{
		"pages/home.liquid":        "<html/>",
		"components/widget.liquid": "<div/>",
		"css/style.css":            "body{}",
		"js/app.js":                "console.log(1)",
		"pages.json":               "[]",
	}

	var inFlight, maxInFlight int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/themes/active/files" {
			entries := make([]themefs.FileTreeEntry, 0, len(files))
			for p := range files {
				entries = append(entries, themefs.FileTreeEntry{Name: p, Path: p, Type: "file"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"files": entries}})
			return
		}

		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		// Delay ensures overlapping reads are observable.
		time.Sleep(75 * time.Millisecond)

		reqPath := strings.TrimPrefix(r.URL.Path, "/store/themes/active/files/")
		content, ok := files[reqPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"path": reqPath, "content": content, "encoding": "utf-8"},
		})
	}))
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	result, err := svc.LoadThemeFiles(context.Background(), svc.store, testStoreAuth(), true)
	if err != nil {
		t.Fatalf("LoadThemeFiles failed: %v", err)
	}

	if _, ok := result["css/style.css"]; !ok {
		t.Error("expected css/style.css to be included with includeAssets: true")
	}
	if _, ok := result["js/app.js"]; !ok {
		t.Error("expected js/app.js to be included with includeAssets: true")
	}
	if _, ok := result["pages.json"]; !ok {
		t.Error("expected pages.json to be included unconditionally, even though it's neither .liquid, .css, nor .js")
	}
	if len(result) != 5 {
		t.Errorf("expected exactly the 2 liquid + 2 asset files + pages.json, got %d: %+v", len(result), result)
	}

	if got := atomic.LoadInt32(&maxInFlight); got < 2 {
		t.Errorf("expected concurrent reads (max in-flight >= 2), got %d — reads look sequential", got)
	}
}

// includeAssets: false keeps .liquid-only behavior; pages.json always included.
func TestLoadThemeFiles_LiquidOnlyByDefault(t *testing.T) {
	ts := newFakeThemeServer(t, map[string]string{
		"pages/home.liquid": "<html/>",
		"css/style.css":     "body{}",
		"pages.json":        "[]",
	})
	defer ts.Close()

	svc := &Service{store: themefs.NewStore(ts.URL)}
	result, err := svc.LoadThemeFiles(context.Background(), svc.store, testStoreAuth(), false)
	if err != nil {
		t.Fatalf("LoadThemeFiles failed: %v", err)
	}
	if _, ok := result["css/style.css"]; ok {
		t.Error("expected css excluded when includeAssets is false")
	}
	if _, ok := result["pages/home.liquid"]; !ok {
		t.Error("expected the liquid file included")
	}
	if _, ok := result["pages.json"]; !ok {
		t.Error("expected pages.json included even when includeAssets is false")
	}
}
