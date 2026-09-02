package ai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// TestGenerate_EditMaterializesWithoutExtraAPICall covers the whole point of
// action "edit": a unique old_string materializes into full "update"
// content with zero extra round trips, and the Result leaving Generate is
// indistinguishable from what a normal "update" proposal would have
// produced — see GeneratedFile's own doc comment on why nothing downstream
// needs to know "edit" ever existed.
func TestGenerate_EditMaterializesWithoutExtraAPICall(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes", map[string]any{
			"summary":             "Updated footer copyright year.",
			"needs_clarification": false,
			"files": []map[string]any{{
				"path": "components/footer.liquid", "action": "edit", "content": "",
				"edits": []map[string]any{{"old_string": "Copyright 2024", "new_string": "Copyright 2025"}},
			}},
			"page_registry_entry":   nil,
			"layout_links_to_add":   []string{},
			"layout_scripts_to_add": []string{},
		}, 50, 20))
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)

	readFile := func(_ context.Context, path string) (string, error) {
		if path != "components/footer.liquid" {
			t.Fatalf("unexpected readFile path %q", path)
		}
		return "<footer>Copyright 2024</footer>", nil
	}

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "update copyright year", nil, nil, nil, readFile)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected the edit to materialize with zero extra API round trips, got %d calls", calls)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %+v", result.Files)
	}
	f := result.Files[0]
	if f.Action != "update" {
		t.Errorf(`expected the materialized file's action to be "update", got %q`, f.Action)
	}
	if f.Content != "<footer>Copyright 2025</footer>" {
		t.Errorf("unexpected materialized content: %q", f.Content)
	}
	if len(f.Edits) != 0 {
		t.Errorf("expected Edits cleared after materialization, got %+v", f.Edits)
	}
}

// editProposeChangesFixture builds the SSE tool-call payload for a
// propose_changes call proposing a single edit-action file — shared by the
// zero-match/multi-match retry tests below, which only differ in what
// old_string the (fake) model sends.
func editProposeChangesFixture(oldString string) map[string]any {
	return map[string]any{
		"summary":             "attempting a fix",
		"needs_clarification": false,
		"files": []map[string]any{{
			"path": "components/footer.liquid", "action": "edit", "content": "",
			"edits": []map[string]any{{"old_string": oldString, "new_string": "replacement"}},
		}},
		"page_registry_entry":   nil,
		"layout_links_to_add":   []string{},
		"layout_scripts_to_add": []string{},
	}
}

func updateProposeChangesFixture(content string) map[string]any {
	return map[string]any{
		"summary":             "fixed",
		"needs_clarification": false,
		"files": []map[string]any{{
			"path": "components/footer.liquid", "action": "update", "content": content, "edits": []map[string]any{},
		}},
		"page_registry_entry":   nil,
		"layout_links_to_add":   []string{},
		"layout_scripts_to_add": []string{},
	}
}

// TestGenerate_ZeroMatchOldStringRetriesNotFails is the "zero matches"
// half of materializeEdits' contract: a materialization failure must not
// end the generation — it's fed back as this propose_changes call's own
// tool_result and the loop continues, exactly like an ordinary failed tool
// call would.
func TestGenerate_ZeroMatchOldStringRetriesNotFails(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes",
				editProposeChangesFixture("text that is not in the file"), 50, 20))
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "0 matches") {
				t.Errorf("expected the retry request to carry the zero-match failure detail, body: %s", body)
			}
			fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", "propose_changes",
				updateProposeChangesFixture("fixed content"), 60, 25))
		default:
			t.Errorf("unexpected 3rd call to the fake Anthropic server")
		}
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	readFile := func(context.Context, string) (string, error) { return "<footer>original</footer>", nil }

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "prompt", nil, nil, nil, readFile)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 API calls (1 failed edit + 1 corrected retry), got %d", calls)
	}
	if result.Summary != "fixed" {
		t.Errorf("expected the eventual corrected result to be returned, got summary %q", result.Summary)
	}
}

// TestGenerate_MultipleMatchesRetriesNotFails is the "several matches" half
// of the same contract.
func TestGenerate_MultipleMatchesRetriesNotFails(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes",
				editProposeChangesFixture("dup"), 50, 20)) // "dup" appears twice in readFile's content below
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "matched 2 times") {
				t.Errorf("expected the retry request to carry the multiple-match failure detail, body: %s", body)
			}
			fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", "propose_changes",
				updateProposeChangesFixture("fixed content"), 60, 25))
		default:
			t.Errorf("unexpected 3rd call to the fake Anthropic server")
		}
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	readFile := func(context.Context, string) (string, error) { return "dup one, dup two", nil }

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "prompt", nil, nil, nil, readFile)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 API calls, got %d", calls)
	}
	if result.Summary != "fixed" {
		t.Errorf("expected the eventual corrected result to be returned, got summary %q", result.Summary)
	}
}

// TestGenerate_TwoFailedEditsFallBackToFullContentRequest covers the bound
// on retries: the same file failing materialization twice must, on its
// SECOND failure, tell the model to resubmit as "update" with full content
// rather than inviting another edit attempt — see
// maxEditMaterializationFailures.
func TestGenerate_TwoFailedEditsFallBackToFullContentRequest(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1, 2:
			fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", calls), fmt.Sprintf("toolu_%d", calls), "propose_changes",
				editProposeChangesFixture("text that is not in the file"), 50, 20))
		case 3:
			// Body is JSON, so a literal `"` inside the message text is
			// escaped to `\"` on the wire — match on a quote-free substring
			// instead of fighting that escaping.
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "resubmit this file with action") {
				t.Errorf("expected the 3rd call's request to carry the fall-back-to-update advice after 2 failures, body: %s", body)
			}
			fmt.Fprint(w, toolUseSSEResponse("msg_3", "toolu_3", "propose_changes",
				updateProposeChangesFixture("fixed content"), 60, 25))
		default:
			t.Errorf("unexpected 4th call to the fake Anthropic server")
		}
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	readFile := func(context.Context, string) (string, error) { return "<footer>original</footer>", nil }

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "prompt", nil, nil, nil, readFile)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected exactly 3 API calls (2 failed edits + 1 full-content retry), got %d", calls)
	}
	if result.Summary != "fixed" {
		t.Errorf("expected the eventual corrected result to be returned, got summary %q", result.Summary)
	}
}
