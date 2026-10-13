package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"ai-chat/internal/aicatalog"
)

// With ThinkingOff, every call says thinking disabled (DeepSeek reasons by default) and sends no effort.
func TestGenerate_ThinkingOffDisablesThinkingOnEveryCall(t *testing.T) {
	type body struct {
		Thinking     *struct{ Type string }   `json:"thinking"`
		OutputConfig *struct{ Effort string } `json:"output_config"`
	}
	for _, off := range []bool{true, false} {
		t.Run(fmt.Sprintf("thinking_off=%v", off), func(t *testing.T) {
			var mu sync.Mutex
			var sent []body
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var b body
				_ = json.Unmarshal(raw, &b)
				mu.Lock()
				sent = append(sent, b)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, toolUseSSEResponse("m", "t", toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
			}))
			defer ts.Close()
			g := catalogueGenerator(t, client(ts.URL), nil)
			tc := ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}, ThinkingOff: off}
			if _, err := g.Generate(context.Background(), tc, nil, "x", nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			b := sent[0]
			if off && (b.Thinking == nil || b.Thinking.Type != "disabled" || b.OutputConfig != nil) {
				t.Errorf("want thinking disabled and no effort, got %+v / %+v", b.Thinking, b.OutputConfig)
			}
			if !off && (b.Thinking == nil || b.Thinking.Type != "adaptive") {
				t.Errorf("want adaptive thinking, got %+v", b.Thinking)
			}
		})
	}
}
