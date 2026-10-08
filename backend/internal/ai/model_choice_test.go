package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"ai-chat/internal/aicatalog"
)

// catalogueGenerator serves the DeepSeek rollback catalogue (optionally edited) through client for its "deepseek" provider.
func catalogueGenerator(t *testing.T, client anthropic.Client, edit func(map[string]any)) *Generator {
	t.Helper()
	data, err := os.ReadFile("../../config/ai-models.deepseek.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(raw)
	}
	data, _ = json.Marshal(raw)
	cat, err := aicatalog.Parse(data, func(string) (string, bool) { return "k", true })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return &Generator{catalog: cat, clients: map[string]anthropic.Client{"deepseek": client}, maxTokens: defaultMaxTokens}
}

type sentRequest struct {
	Model    string `json:"model"`
	Thinking *struct {
		Type string `json:"type"`
	} `json:"thinking"`
	OutputConfig *struct {
		Effort string `json:"effort"`
	} `json:"output_config"`
}

// proposingServer answers every call with an accepted propose_changes and records each request.
func proposingServer(t *testing.T, sent *[]sentRequest) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req sentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		*sent = append(*sent, req)
		n := len(*sent)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, toolUseSSEResponse(fmt.Sprintf("msg_%d", n), fmt.Sprintf("toolu_%d", n), toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
	}))
}

func client(url string) anthropic.Client {
	return anthropic.NewClient(option.WithBaseURL(url), option.WithAPIKey("k"))
}

func TestGenerate_ChosenModelAndEffortReachTheProvider(t *testing.T) {
	var sent []sentRequest
	ts := proposingServer(t, &sent)
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "high"}}, nil, "x", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := sent[0]
	if got.Model != "deepseek-v4-pro" || got.Thinking == nil || got.Thinking.Type != "adaptive" || got.OutputConfig == nil || got.OutputConfig.Effort != "high" {
		t.Errorf("want deepseek-v4-pro, adaptive thinking, effort high; got %+v", got)
	}
	if result.ModelID != "deepseek-pro" || result.Effort != "high" {
		t.Errorf("result should record the answering model, got %q/%q", result.ModelID, result.Effort)
	}
}

func TestGenerate_ModelWithoutThinkingGetsNeitherParameter(t *testing.T) {
	var sent []sentRequest
	ts := proposingServer(t, &sent)
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), func(m map[string]any) {
		flash := m["models"].([]any)[1].(map[string]any)
		flash["thinking"], flash["efforts"], flash["default_effort"] = false, []any{}, ""
		m["auto"].(map[string]any)["design_effort"] = ""
	})

	if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash"}}, nil, "x", nil, nil, nil, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := sent[0]; got.Model != "deepseek-v4-flash" || got.Thinking != nil || got.OutputConfig != nil {
		t.Errorf("want no thinking and no effort for a model without thinking, got %+v", got)
	}
}

func TestGenerate_ImageTurnUsesTheVisionModel(t *testing.T) {
	var sent []sentRequest
	ts := proposingServer(t, &sent)
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)
	imgs := []Image{{Base64: "aGk=", MediaType: "image/png"}}

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "high"}}, nil, "x", imgs, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Vision offers only "low", so the requested "high" falls back to its default.
	if got := sent[0]; got.Model != "deepseek-v4-flash-vision-exp" || got.OutputConfig == nil || got.OutputConfig.Effort != "low" {
		t.Errorf("want the vision model at low, got %+v", got)
	}
	if result.ModelID != "deepseek-flash-vision" {
		t.Errorf("the vision model answered, got %q", result.ModelID)
	}
}

func TestGenerate_RepairKeepsTheTurnsModelAndEffort(t *testing.T) {
	var sent []sentRequest
	ts := proposingServer(t, &sent)
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)
	tc := ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "medium"}}

	first, err := g.Generate(context.Background(), tc, nil, "x", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	tc.Continue = first.Conversation()
	tc.Model = aicatalog.Choice{} // a repair carries no choice of its own
	if _, err := g.Generate(context.Background(), tc, nil, "repair", nil, nil, nil, nil); err != nil {
		t.Fatalf("repair Generate: %v", err)
	}
	if got := sent[1]; got.Model != "deepseek-v4-pro" || got.OutputConfig == nil || got.OutputConfig.Effort != "medium" {
		t.Errorf("repair must use the turn's model and effort, got %+v", got)
	}
}

func TestSummarize_UsesTheSummaryModel(t *testing.T) {
	var body map[string]any
	ts := summarizeServer(t, []map[string]any{{"type": "text", "text": "summary"}}, &body)
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)

	if _, err := g.Summarize(context.Background(), []Turn{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if body["model"] != "deepseek-v4-flash" {
		t.Errorf("want summary_model deepseek-v4-flash, got %v", body["model"])
	}
	if thinking, _ := body["thinking"].(map[string]any); thinking["type"] != "disabled" {
		t.Errorf("summaries keep thinking off, got %v", body["thinking"])
	}
}
