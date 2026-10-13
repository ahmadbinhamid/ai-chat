package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-chat/internal/aicatalog"
)

// A thinking-off turn that keeps exploring gets one nudge after designNudgeAfterRounds exploration rounds and can
// then propose on a normal round; a thinking-on turn never sees it and is forced at the usual limit.
func TestGenerate_DesignNudgeAfterExplorationRounds(t *testing.T) {
	for _, thinkingOff := range []bool{true, false} {
		t.Run(fmt.Sprintf("thinking_off=%v", thinkingOff), func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(raw))
				n := len(bodies)
				mu.Unlock()
				var req struct {
					ToolChoice struct{ Type string } `json:"tool_choice"`
				}
				_ = json.Unmarshal(raw, &req)
				w.Header().Set("Content-Type", "text/event-stream")
				if strings.Contains(string(raw), "You have explored for several rounds") || req.ToolChoice.Type == "tool" {
					fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("m%d", n), fmt.Sprintf("t%d", n), toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
					return
				}
				fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("m%d", n), fmt.Sprintf("t%d", n), toolNameReadThemeFile, map[string]any{"path": fmt.Sprintf("components/c%d.liquid", n)}, 10, 5))
			}))
			defer ts.Close()
			g := catalogueGenerator(t, client(ts.URL), nil)
			toolExec := func(context.Context, string, json.RawMessage) (string, error) { return "<div></div>", nil }
			tc := ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}, ThinkingOff: thinkingOff}
			if _, err := g.Generate(context.Background(), tc, nil, "make the header dark", nil, nil, toolExec, nil); err != nil {
				t.Fatal(err)
			}
			nudged := 0
			for _, b := range bodies {
				if strings.Contains(b, "You have explored for several rounds") {
					nudged++
				}
			}
			if thinkingOff {
				if len(bodies) != designNudgeAfterRounds+1 {
					t.Errorf("want a proposal on round %d right after the nudge, got %d calls", designNudgeAfterRounds+1, len(bodies))
				}
				if nudged != 1 {
					t.Errorf("want the nudge in exactly the last request, seen in %d", nudged)
				}
				return
			}
			if nudged != 0 || len(bodies) != forceProposeAfterRounds+1 {
				t.Errorf("thinking on: want no nudge and the usual forced round, got nudged=%d calls=%d", nudged, len(bodies))
			}
		})
	}
}
