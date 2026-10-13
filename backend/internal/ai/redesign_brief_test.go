package ai

import (
	"bytes"
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

// The redesign brief rides only in a redesign turn's user message, after the prompt; the system blocks stay
// byte-identical to an ordinary turn's, so the cached prefix is shared.
func TestGenerate_RedesignBriefOnlyInTheUserTurn(t *testing.T) {
	type request struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	var mu sync.Mutex
	var requests []request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse("m", "t", toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
	}))
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)
	history := []Turn{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "Hello."}}
	for _, redesign := range []bool{false, true} {
		tc := ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}, Redesign: redesign}
		if _, err := g.Generate(context.Background(), tc, history, "redesign the homepage", nil, nil, nil, nil); err != nil {
			t.Fatalf("Generate(redesign=%v): %v", redesign, err)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("want 2 requests, got %d", len(requests))
	}
	normal, redesign := requests[0], requests[1]

	if !bytes.Equal(normal.System, redesign.System) {
		t.Error("a redesign turn's system blocks differ from an ordinary turn's")
	}
	if strings.Contains(string(redesign.System), "Redesign brief") {
		t.Error("the redesign brief must not be in the system prompt")
	}
	for _, m := range normal.Messages {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "Redesign brief") {
				t.Errorf("an ordinary turn carries the redesign brief in a %s message", m.Role)
			}
		}
	}
	last := redesign.Messages[len(redesign.Messages)-1]
	if last.Role != "user" || len(last.Content) != 2 || last.Content[0].Text != "redesign the homepage" || last.Content[1].Text != redesignBrief {
		t.Errorf("want the prompt then the brief in the redesign turn's user message, got %+v", last)
	}
	if len(redesign.Messages) != len(normal.Messages) {
		t.Errorf("the brief must join the user message, not add one: %d vs %d messages", len(redesign.Messages), len(normal.Messages))
	}
}

func TestRedesignBrief_StaysSmall(t *testing.T) {
	if n := len(redesignBrief); n == 0 || n > 3072 {
		t.Errorf("redesign brief is %d bytes, want 1-3072", n)
	}
}
