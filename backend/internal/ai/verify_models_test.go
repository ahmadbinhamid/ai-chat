package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ai-chat/internal/aicatalog"
)

// modelListGenerator builds a Generator from catalogueFile whose first provider is served at baseURL.
func modelListGenerator(t *testing.T, catalogueFile, baseURL string, edit func(map[string]any)) *Generator {
	t.Helper()
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, p := range raw["providers"].(map[string]any) {
		p.(map[string]any)["base_url"] = baseURL
	}
	if edit != nil {
		edit(raw)
	}
	data, _ = json.Marshal(raw)
	lookup := func(string) (string, bool) { return "k", true }
	cat, err := aicatalog.Parse(data, lookup)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, err := New(cat, lookup, 0, StreamTimeouts{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

// modelListServer answers GET /v1/models in OpenRouter's shape with ids, counting the calls.
func modelListServer(t *testing.T, status int, ids ...string) (*httptest.Server, *int) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		data := []map[string]any{}
		for _, id := range ids {
			data = append(data, map[string]any{"id": id, "name": id, "created": 1767225600, "context_length": 1048576})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

var openRouterModels = []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-flash-vision-exp", "other/model"}

func TestVerifyModels(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		ids     []string
		edit    func(map[string]any)
		wantErr string
	}{
		{name: "every model listed", status: http.StatusOK, ids: openRouterModels},
		{name: "a typo fails", status: http.StatusOK, ids: openRouterModels, edit: func(raw map[string]any) {
			raw["models"].([]any)[1].(map[string]any)["model"] = "deepseek/deepseek-v4-flsh"
		}, wantErr: `deepseek-flash ("deepseek/deepseek-v4-flsh")`},
		{name: "a model the list dropped fails", status: http.StatusOK, ids: openRouterModels[1:], wantErr: "deepseek-pro"},
		{name: "list unavailable only warns", status: http.StatusServiceUnavailable},
		{name: "empty list only warns", status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, calls := modelListServer(t, tt.status, tt.ids...)
			g := modelListGenerator(t, "../../config/ai-models.json", ts.URL, tt.edit)
			err := g.VerifyModels(context.Background())
			if tt.wantErr == "" && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("want an error naming %s, got %v", tt.wantErr, err)
			}
			if *calls == 0 {
				t.Error("want the model list fetched")
			}
		})
	}
}

func TestVerifyModels_UnreachableProviderOnlyWarns(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	g := modelListGenerator(t, "../../config/ai-models.json", url, nil)
	if err := g.VerifyModels(context.Background()); err != nil {
		t.Fatalf("want startup to carry on when the provider can't be reached, got %v", err)
	}
}

// The DeepSeek rollback catalogue doesn't set verify_models, so it never depends on DeepSeek serving a model list.
func TestVerifyModels_SkipsProvidersWithoutTheFlag(t *testing.T) {
	ts, calls := modelListServer(t, http.StatusOK, "something-else")
	g := modelListGenerator(t, "../../config/ai-models.deepseek.json", ts.URL, nil)
	if err := g.VerifyModels(context.Background()); err != nil || *calls != 0 {
		t.Fatalf("want no check for a provider without verify_models, got err=%v calls=%d", err, *calls)
	}
}
