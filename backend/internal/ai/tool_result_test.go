package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-chat/internal/themefs"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestToolResultBlock(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		isError     bool
		wantText    string
		wantIsError bool
	}{
		{"success unchanged", "### a.liquid\n<div></div>", false, "### a.liquid\n<div></div>", false},
		{"error prefixed", "invalid pattern: missing )", true, "ERROR: invalid pattern: missing )", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(toolResultBlock("toolu_1", tt.text, tt.isError))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"is_error"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got.Content) != 1 || got.Content[0].Text != tt.wantText {
				t.Errorf("text = %+v, want %q", got.Content, tt.wantText)
			}
			if got.IsError != tt.wantIsError {
				t.Errorf("is_error = %v, want %v", got.IsError, tt.wantIsError)
			}
		})
	}
}

// toolResultTexts returns every tool_result text block in a captured Messages request body.
func toolResultTexts(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	var texts []string
	for _, m := range req.Messages {
		var blocks []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			for _, c := range b.Content {
				texts = append(texts, c.Text)
			}
		}
	}
	return texts
}

func TestGenerate_MaterializeFailureToolResultPrefixedWithError(t *testing.T) {
	calls := 0
	var retryBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes",
				editProposeChangesFixture("text that is not in the file"), 50, 20))
		default:
			retryBody, _ = io.ReadAll(r.Body)
			fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", "propose_changes",
				updateProposeChangesFixture("corrected content"), 60, 25))
		}
	}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key")))
	readFile := func(context.Context, string) (string, error) { return "<footer>original</footer>", nil }

	if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "prompt", nil, nil, nil, readFile); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	texts := toolResultTexts(t, retryBody)
	if len(texts) != 1 || !strings.HasPrefix(texts[0], "ERROR: ") {
		t.Fatalf("expected one tool_result starting with ERROR:, got %q", texts)
	}
}

func TestGenerate_ToolExecErrorPrefixedSuccessUnchanged(t *testing.T) {
	calls := 0
	var secondBody, thirdBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", toolNameGrepTheme,
				map[string]any{"pattern": "("}, 50, 20))
		case 2:
			secondBody, _ = io.ReadAll(r.Body)
			fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", toolNameGrepTheme,
				map[string]any{"pattern": "footer"}, 50, 20))
		default:
			thirdBody, _ = io.ReadAll(r.Body)
			fmt.Fprint(w, toolUseSSEResponse("msg_3", "toolu_3", "propose_changes",
				updateProposeChangesFixture("corrected content"), 60, 25))
		}
	}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key")))
	toolExec := func(_ context.Context, _ string, input json.RawMessage) (string, error) {
		if strings.Contains(string(input), `"("`) {
			return "", errors.New("invalid pattern: missing closing )")
		}
		return "components/footer.liquid:1: <footer>", nil
	}

	if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "prompt", nil, nil, toolExec, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if texts := toolResultTexts(t, secondBody); len(texts) != 1 || texts[0] != "ERROR: invalid pattern: missing closing )" {
		t.Fatalf("expected the executor error prefixed with ERROR:, got %q", texts)
	}
	texts := toolResultTexts(t, thirdBody)
	if len(texts) != 2 || texts[1] != "components/footer.liquid:1: <footer>" {
		t.Fatalf("expected the successful result unchanged, got %q", texts)
	}
}

// The model must learn the allowed create targets before proposing, from the same source the rejection uses.
func TestStaticSystemPrompt_StatesAllowedFileTypes(t *testing.T) {
	text := staticSystemPromptBlock().Text
	for _, want := range []string{themefs.GeneratedFileTypes(), "never an image (.svg"} {
		if !strings.Contains(text, want) {
			t.Errorf("static system prompt missing %q", want)
		}
	}
}
