package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestDecodeProposeInput_StringEncodedFields(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   bool
		wantEntry string // page_registry_entry slug, "" for nil
		wantFiles int
	}{
		{"well-formed input is unchanged",
			`{"summary":"s","files":[{"path":"a.css","action":"create","content":"x"}],"page_registry_entry":{"slug":"faq"}}`, false, "faq", 1},
		{"empty-string page_registry_entry means none",
			`{"summary":"s","files":[],"page_registry_entry":""}`, false, "", 0},
		{"\"null\" page_registry_entry means none",
			`{"summary":"s","files":[],"page_registry_entry":"null"}`, false, "", 0},
		{"page_registry_entry encoded as a JSON string",
			`{"summary":"s","files":[],"page_registry_entry":"{\"slug\":\"faq\",\"page\":\"faq\"}"}`, false, "faq", 0},
		{"files encoded as a JSON string",
			`{"summary":"s","files":"[{\"path\":\"a.css\",\"action\":\"create\",\"content\":\"x\"}]"}`, false, "", 1},
		{"empty-string arrays mean none",
			`{"summary":"s","files":"","layout_links_to_add":"","layout_scripts_to_add":"","use_attachments":""}`, false, "", 0},
		{"an unreadable page_registry_entry string is dropped (the server synthesizes one)",
			`{"summary":"s","files":[],"page_registry_entry":"the faq page"}`, false, "", 0},
		{"an object string where an array belongs still fails",
			`{"summary":"s","files":"{\"path\":\"a.css\"}"}`, true, "", 0},
		{"a wrong type on another field still fails",
			`{"summary":"s","files":[],"answered_question":"yes"}`, true, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeProposeInput(json.RawMessage(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			slug := ""
			if got.PageRegistryEntry != nil {
				slug = got.PageRegistryEntry.Slug
			}
			if slug != tt.wantEntry || len(got.Files) != tt.wantFiles {
				t.Fatalf("decoded entry %q / %d files, want %q / %d", slug, len(got.Files), tt.wantEntry, tt.wantFiles)
			}
		})
	}
}

// sentBodies returns the request bodies s has recorded so far.
func sentBodies(s *scriptedServer) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.bodies))
	for i, b := range s.bodies {
		out[i] = string(b)
	}
	return out
}

func answerProposal(extra map[string]any) map[string]any {
	p := map[string]any{"summary": "Here's the answer.", "answered_question": true, "files": []any{}}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// The Nemotron case: page_registry_entry sent as "" and files as a JSON string are accepted on the first call.
func TestGenerate_AcceptsStringEncodedProposalFields(t *testing.T) {
	srv, ts := newScriptedServer(t,
		toolUseSSEResponse("msg_1", "toolu_1", toolNameProposeChanges, answerProposal(map[string]any{
			"page_registry_entry": "", "files": "[]",
		}), 10, 5))
	defer ts.Close()
	bodies := func() []string { return sentBodies(srv) }
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "what fonts do I use?", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(bodies()) != 1 || result.PageRegistryEntry != nil || len(result.Files) != 0 {
		t.Fatalf("calls %d, entry %v, files %d; want one call, no entry, no files", len(bodies()), result.PageRegistryEntry, len(result.Files))
	}
}

// A proposal that still won't decode goes back to the model as its tool result, and the turn continues.
func TestGenerate_HandsAnUndecodableProposalBack(t *testing.T) {
	srv, ts := newScriptedServer(t,
		toolUseSSEResponse("msg_1", "toolu_1", toolNameProposeChanges, answerProposal(map[string]any{
			"files": `{"path":"a.css"}`,
		}), 10, 5),
		toolUseSSEResponse("msg_2", "toolu_2", toolNameProposeChanges, answerProposal(nil), 10, 5))
	defer ts.Close()
	bodies := func() []string { return sentBodies(srv) }
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "what fonts do I use?", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate failed: %v; an undecodable proposal must not end the turn", err)
	}
	if result.Summary != "Here's the answer." {
		t.Fatalf("summary = %q, want the resent proposal's", result.Summary)
	}
	sent := bodies()
	if len(sent) != 2 {
		t.Fatalf("model calls = %d, want 2 (the bad proposal, then its resend)", len(sent))
	}
	second := sent[1]
	if !strings.Contains(second, `"tool_use_id":"toolu_1"`) || !strings.Contains(second, "could not be read") ||
		!strings.Contains(second, `"is_error":true`) {
		t.Fatalf("the second call should carry the decode error as toolu_1's error result:\n%s", second)
	}
}

// The same undecodable proposal sent twice in a row ends the turn instead of burning every remaining round.
func TestGenerate_StopsWhenTheSameUndecodableProposalComesBack(t *testing.T) {
	bad := answerProposal(map[string]any{"files": `{"path":"a.css"}`})
	srv, ts := newScriptedServer(t,
		toolUseSSEResponse("msg_1", "toolu_1", toolNameProposeChanges, bad, 10, 5),
		toolUseSSEResponse("msg_2", "toolu_2", toolNameProposeChanges, bad, 10, 5))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))

	_, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "what fonts do I use?", nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "resent the same undecodable") {
		t.Fatalf("err = %v, want the repeat to end the turn", err)
	}
	if n := len(sentBodies(srv)); n != 2 {
		t.Fatalf("model calls = %d, want 2", n)
	}
}

// Nemotron's case: a page_registry_entry it can't express as an object no longer sinks its file changes.
func TestGenerate_KeepsFilesWhenPageRegistryEntryIsUnreadable(t *testing.T) {
	srv, ts := newScriptedServer(t,
		toolUseSSEResponse("msg_1", "toolu_1", toolNameProposeChanges, map[string]any{
			"summary":             "Added the FAQ page.",
			"files":               []map[string]any{{"path": "pages/faq.liquid", "action": "create", "content": "<h1>FAQ</h1>"}},
			"page_registry_entry": "faq page at /faq",
		}, 10, 5))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "add a faq page", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(result.Files) != 1 || result.PageRegistryEntry != nil || len(sentBodies(srv)) != 1 {
		t.Fatalf("files %d, entry %v, calls %d; want the file kept, the entry dropped, one call",
			len(result.Files), result.PageRegistryEntry, len(sentBodies(srv)))
	}
}
