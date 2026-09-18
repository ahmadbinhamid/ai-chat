package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-chat/internal/themefs"

	"github.com/anthropics/anthropic-sdk-go"
)

func themefsManifestForTest() *themefs.Manifest {
	return &themefs.Manifest{
		Components: []themefs.ComponentInfo{
			{Path: "components/z.liquid", Params: []string{"b", "a"}},
			{Path: "components/a.liquid", Params: []string{"title"}},
		},
	}
}

func TestMustDisableThinking_DeepSeekModes(t *testing.T) {
	t.Parallel()
	g := &Generator{provider: "deepseek", modelName: "deepseek-v4-pro"}
	tests := []struct {
		name string
		tc   ThemeContext
		want bool
	}{
		{"edit keeps thinking", ThemeContext{}, false},
		{"pages keeps thinking", ThemeContext{GenerationMode: GenerationModePages}, false},
		{"simple_edit disables", ThemeContext{SimpleEditOneShot: true}, true},
		{"page_create disables", ThemeContext{PageCreatePrepared: true}, true},
		{"repair disables", ThemeContext{Repair: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := g.mustDisableThinking(tt.tc); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
	anth := &Generator{provider: "anthropic"}
	if anth.mustDisableThinking(ThemeContext{Repair: true, SimpleEditOneShot: true}) {
		t.Fatal("anthropic must not force-disable thinking via DeepSeek policy")
	}
}

func TestCanForceNamedToolChoice(t *testing.T) {
	t.Parallel()
	ds := &Generator{provider: "deepseek"}
	if ds.canForceNamedToolChoice(ThemeContext{}) {
		t.Fatal("deepseek+thinking must not force named tool")
	}
	if !ds.canForceNamedToolChoice(ThemeContext{Repair: true}) {
		t.Fatal("deepseek+repair (thinking off) may force named tool")
	}
	anth := &Generator{provider: "anthropic"}
	if !anth.canForceNamedToolChoice(ThemeContext{}) {
		t.Fatal("anthropic may always force")
	}
}

func TestApplyCacheControl_OmittedForDeepSeek(t *testing.T) {
	t.Parallel()
	ds := &Generator{provider: "deepseek"}
	block := anthropic.TextBlockParam{Text: "stable"}
	ds.applyCacheControlEphemeral(&block)
	if block.CacheControl.TTL != "" || block.CacheControl.Type != "" {
		// CacheControl zero value — Type empty means unset.
		t.Fatalf("deepseek must omit cache_control, got %+v", block.CacheControl)
	}
	anth := &Generator{provider: "anthropic"}
	anth.applyCacheControlEphemeral(&block)
	if block.CacheControl.Type == "" {
		t.Fatal("anthropic must set cache_control")
	}
}

func TestStaticSystemPrompt_PrefixStable(t *testing.T) {
	t.Parallel()
	g := &Generator{provider: "deepseek"}
	a := g.staticSystemPromptBlock()
	b := g.staticSystemPromptBlock()
	if a.Text != b.Text {
		t.Fatal("static system prompt must be byte-identical across calls")
	}
	if !strings.Contains(a.Text, "<theme_engine_spec>") {
		t.Fatal("expected embedded theme engine spec")
	}
	if a.CacheControl.Type != "" {
		t.Fatal("deepseek static block must not carry cache_control")
	}
}

func TestFormatManifest_StableOrder(t *testing.T) {
	t.Parallel()
	m := themefsManifestForTest()
	a := formatManifest(m)
	b := formatManifest(m)
	if a != b {
		t.Fatal("formatManifest must be deterministic")
	}
	if !strings.Contains(a, "components/a.liquid") || !strings.Contains(a, "components/z.liquid") {
		t.Fatalf("unexpected manifest render: %s", a)
	}
	// a.liquid must appear before z.liquid after sort.
	if strings.Index(a, "components/a.liquid") > strings.Index(a, "components/z.liquid") {
		t.Fatalf("components not sorted by path:\n%s", a)
	}
}

func TestModeBudgetsReachResolve(t *testing.T) {
	t.Parallel()
	budgets := DefaultTokenBudgets()
	cases := []struct {
		name       string
		tc         ThemeContext
		wantTokens int64
		wantEffort anthropic.OutputConfigEffort
	}{
		{"simple_edit", ThemeContext{SimpleEditOneShot: true}, 8_000, anthropic.OutputConfigEffortMedium},
		{"repair", ThemeContext{Repair: true}, budgets.Repair, anthropic.OutputConfigEffortMedium},
		{"pages/full_page", ThemeContext{GenerationMode: GenerationModePages}, budgets.Complex, anthropic.OutputConfigEffortHigh},
		{"edit/section", ThemeContext{}, budgets.Interactive, anthropic.OutputConfigEffortMedium},
		{"brand", ThemeContext{GenerationMode: GenerationModeBrand}, budgets.Brand, anthropic.OutputConfigEffortLow},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveMaxTokens(tt.tc, 64_000, budgets); got != tt.wantTokens {
				t.Fatalf("tokens got %d want %d", got, tt.wantTokens)
			}
			if got := resolveEffort(tt.tc, ""); got != tt.wantEffort {
				t.Fatalf("effort got %q want %q", got, tt.wantEffort)
			}
		})
	}
}

func TestApplyThinkingConfig_DeepSeekRepairDisabled(t *testing.T) {
	t.Parallel()
	g := &Generator{provider: "deepseek", model: "deepseek-v4-pro", effort: anthropic.OutputConfigEffortHigh}
	var params anthropic.MessageNewParams
	g.applyThinkingConfig(&params, ThemeContext{Repair: true}, g.model, anthropic.OutputConfigEffortMedium)
	if params.Thinking.OfDisabled == nil {
		t.Fatal("expected thinking disabled for deepseek repair")
	}
	if params.OutputConfig.Effort != "" {
		t.Fatalf("effort must not be sent when thinking disabled, got %q", params.OutputConfig.Effort)
	}
}

func TestUsageTokenAttrs_Availability(t *testing.T) {
	t.Parallel()
	// Missing optional fields must report *_available=false, not invent zeros as "present".
	var usage anthropic.Usage
	attrs := usageTokenAttrs(usage)
	joined, _ := json.Marshal(attrs)
	s := string(joined)
	for _, want := range []string{"reasoning_tokens_available", "cache_read_input_tokens_available", "cache_creation_input_tokens_available"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
}

func TestMaxZeroToolNudges_Bounded(t *testing.T) {
	t.Parallel()
	if maxZeroToolNudges < 1 || maxZeroToolNudges > 3 {
		t.Fatalf("maxZeroToolNudges out of expected band: %d", maxZeroToolNudges)
	}
	if streamAccumulateMaxAttempts != 2 {
		t.Fatalf("provider stream attempts must stay bounded at 2, got %d", streamAccumulateMaxAttempts)
	}
}

func TestEstimateToolsSchemaBytes_Deterministic(t *testing.T) {
	t.Parallel()
	tools := toolsForContext(ThemeContext{Repair: true})
	a := estimateToolsSchemaBytes(tools)
	b := estimateToolsSchemaBytes(tools)
	if a == 0 || a != b {
		t.Fatalf("schema bytes a=%d b=%d", a, b)
	}
}

func TestContextCancel_NoExtraRetryClassification(t *testing.T) {
	t.Parallel()
	if isRetryableStreamErr(context.Canceled) {
		t.Fatal("canceled must not retry")
	}
	if isRetryableStreamErr(context.DeadlineExceeded) {
		t.Fatal("deadline must not retry")
	}
}
