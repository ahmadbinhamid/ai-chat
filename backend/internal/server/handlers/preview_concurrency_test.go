package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/storefrontquery"
	"ai-chat/internal/themefs"
)

const slowLookupDelay = 300 * time.Millisecond

// slowFlowposServer delays the store, menu and product-list lookups by slowLookupDelay each; anything else 404s.
func slowFlowposServer(t *testing.T) *httptest.Server {
	t.Helper()
	products := rawProductsResponse(t, 1, 1, []map[string]any{sampleRealProduct(1, "Real Widget", "real-widget")})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/store":
			time.Sleep(slowLookupDelay)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"store": map[string]any{"name": "Fleure"}}})
		case "/store/themes/active/files/defaults.json":
			time.Sleep(slowLookupDelay)
			content := `{"menu": {"items": [{"label": "Real Link", "url": "/real"}]}}`
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"path": "defaults.json", "content": content, "encoding": "utf-8"}})
		case "/products":
			time.Sleep(slowLookupDelay)
			_, _ = w.Write(products)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestPreviewContexts_FetchStoreMenuAndProductsConcurrently(t *testing.T) {
	ts := slowFlowposServer(t)
	defer ts.Close()
	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)
	storeAuth := themefs.RequestAuth{Token: "t", TenantID: 1}

	tests := []struct {
		name  string
		build func() map[string]any
	}{
		{name: "default context", build: func() map[string]any {
			return buildPreviewContext(context.Background(), buildSvc, storeAuth, 2*time.Second)
		}},
		{name: "page context", build: func() map[string]any {
			page := storefrontquery.Parse(map[string][]string{"path": {"shop"}})
			return buildPageContext(context.Background(), buildSvc, storeAuth, 2*time.Second, page)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			ctx := tt.build()
			// Run one after another these three lookups take 3x the delay; concurrently, about 1x.
			if elapsed := time.Since(start); elapsed >= 2*slowLookupDelay {
				t.Errorf("took %v; want under %v, as if the lookups overlapped", elapsed, 2*slowLookupDelay)
			}
			if store, _ := ctx["store"].(map[string]any); store["name"] != "Fleure" {
				t.Errorf("want the real store name, got %+v", ctx["store"])
			}
			menu, _ := ctx["menu"].(map[string]any)
			if items, _ := menu["items"].([]any); len(items) != 1 {
				t.Errorf("want the real one-item menu, got %+v", ctx["menu"])
			}
		})
	}
}

func TestPreviewContext_SlowStoreLookupFallsBackToFixture(t *testing.T) {
	ts := slowFlowposServer(t)
	defer ts.Close()
	buildSvc := themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)

	start := time.Now()
	ctx := buildPreviewContext(context.Background(), buildSvc, themefs.RequestAuth{Token: "t", TenantID: 1}, slowLookupDelay/3)
	if elapsed := time.Since(start); elapsed >= slowLookupDelay {
		t.Errorf("took %v; want the timeout to cut every lookup short", elapsed)
	}
	fixture := themefs.FixtureContext()
	if ctx["store"].(map[string]any)["name"] != fixture["store"].(map[string]any)["name"] {
		t.Errorf("want the fixture store name after a timeout, got %+v", ctx["store"])
	}
	assertPreviewContextKeySet(t, ctx)
}
