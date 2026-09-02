// Package ai wraps the Anthropic Claude API to turn a merchant's chat prompt
// into theme file changes (Liquid pages/components, CSS, JS), following the
// engine convention in prompts/theme_engine_spec.md. The model drives a
// short read/explore tool loop (list_theme_files/read_theme_file/grep_theme)
// before finalizing a turn via the propose_changes tool, so it edits an
// existing file having actually seen it rather than guessing — see
// tools.go for the tool definitions and Generate below for the loop.
package ai

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"ai-chat/internal/themefs"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// themeEngineSpec is THEME_ENGINE_SPEC.md, embedded at build time so this
// service is self-contained — it does not read the spec from a sibling
// repo's storage path at runtime. Keep prompts/theme_engine_spec.md in sync
// with the canonical copy when the theme engine convention changes.
//
//go:embed prompts/theme_engine_spec.md
var themeEngineSpec string

// Turn is one prior turn of the conversation, replayed as grounding for the
// next call.
type Turn struct {
	Role    string // "user" or "assistant"
	Content string
}

// GeneratedFile is one file the model proposes creating, updating, or
// editing. Action "edit" is a wire-format optimization only — see
// materializeEdits, which Generate calls immediately after parsing
// propose_changes' input: by the time a *Result leaves Generate, every file
// is "create" or "update" with real Content, and Edits is always empty.
// Nothing downstream of Generate (themebuild, themecheck, the write plan)
// ever sees "edit" — this field's zero value (nil) already behaves as "no
// edits", so a fake generator or eval fixture built directly in Go, never
// through JSON, needs no special-casing.
type GeneratedFile struct {
	Path    string `json:"path"`
	Action  string `json:"action"` // "create" | "update" | "edit"
	Content string `json:"content"`
	Edits   []Edit `json:"edits"`
}

// Edit is one find/replace pair for GeneratedFile's "edit" action —
// old_string must match the file's current content exactly once (see
// applyEdits); new_string may be empty (a deletion).
type Edit struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// Result is the model's final answer for a turn, delivered as the input of
// a propose_changes tool call rather than the response body itself (see
// Generate) — the shape is unchanged from when Structured Outputs enforced
// it directly.
type Result struct {
	Summary            string             `json:"summary"`
	NeedsClarification bool               `json:"needs_clarification"`
	Files              []GeneratedFile    `json:"files"`
	PageRegistryEntry  *themefs.PageEntry `json:"page_registry_entry"`
	LayoutLinksToAdd   []string           `json:"layout_links_to_add"`
	LayoutScriptsToAdd []string           `json:"layout_scripts_to_add"`
	InputTokens        int64              `json:"-"`
	OutputTokens       int64              `json:"-"`
}

// GenerationMode restricts what a turn is allowed to touch — see
// theme_engine_spec.md's scaffold flow (phase 7): a brand-new theme is
// built as three separate turns, each narrower than a normal edit.
const (
	// GenerationModeEdit is the default, unrestricted mode: any tool, any
	// file. The empty string means this too (see dynamicSystemPrompt/
	// toolsForMode) so existing callers that never set GenerationMode don't
	// need to change.
	GenerationModeEdit  = "edit"
	GenerationModeBrand = "brand" // only defaults.json, only propose_changes
	GenerationModeCopy  = "copy"  // hardcoded component/page text only
	GenerationModePages = "pages" // adding new pages.json-registered pages
)

// ThemeContext is the current theme state given to Claude as grounding —
// only what composing/editing a page actually needs (THEME_ENGINE_SPEC.md
// §5/§6), not a dump of every file's content (the model fetches content
// itself via read_theme_file — see Generate).
type ThemeContext struct {
	ThemeSlug    string
	PagesJSON    string // current pages.json content, or "" if none yet
	DefaultsJSON string // current defaults.json content
	// FileTree is the theme's current file listing (paths only, no
	// content) — supplied up front so a turn that doesn't need to explore
	// beyond what it already knows about doesn't have to spend its first
	// tool call on list_theme_files. The tool remains available to re-list
	// if this feels stale (e.g. after several turns).
	FileTree []themefs.FileTreeEntry
	// Manifest indexes every existing component/partial's inferred
	// call-param signature (phase 6 — see themefs.Store.GetOrGenerateManifest),
	// so the model can see what params an existing component actually
	// expects instead of guessing from its name alone. Nil is fine (no
	// manifest available) — the model still has read_theme_file/
	// grep_theme to work it out by reading the component directly.
	Manifest *themefs.Manifest
	// GenerationMode restricts what this turn may touch — see the
	// GenerationMode* constants. Empty behaves as GenerationModeEdit.
	GenerationMode string
}

// Generator calls Claude to produce theme file changes.
type Generator struct {
	client anthropic.Client
	model  anthropic.Model
	effort anthropic.OutputConfigEffort
	// fake, when true, makes Generate return a canned Result after
	// fakeDelay — no Anthropic API call, no tokens spent — see NewFake and
	// its doc comment for what this is for.
	fake      bool
	fakeDelay time.Duration
	// maxTokens is the Claude call's max_tokens — see defaultMaxTokens and
	// config.Config.AnthropicMaxTokens (ANTHROPIC_MAX_TOKENS env var).
	maxTokens int64
}

// New constructs the client. apiKey empty is a configuration error the
// caller should surface at startup (this service has no "run without AI"
// mode — generation is the entire product), not something to silently
// degrade around. maxTokens <= 0 falls back to defaultMaxTokens.
//
// baseURL empty targets Anthropic's real API, exactly as before this param
// was added. A non-empty baseURL points the same anthropic-sdk-go client at
// any Anthropic Messages-API-compatible endpoint instead — e.g. DeepSeek's
// documented compat proxy (https://api.deepseek.com/anthropic, confirmed
// live: streaming and thinking/effort are supported there; cache_control is
// silently ignored, not an error). This is why Generate/tools.go/
// resultSchema need zero provider-specific code: the wire protocol is the
// same, only the endpoint and model string differ.
//
// tool_choice forcing is NOT reliably honored by DeepSeek's compat endpoint,
// despite being accepted without error — confirmed empirically: a plain
// "hello" with tool_choice: any still gets a toolless conversational
// end_turn reply. Real Anthropic guarantees at least one tool call under
// OfAny; DeepSeek's model will still reason its way to skipping one anyway.
// See Generate's "len(toolUses) == 0" handling, which nudges rather than
// fails outright specifically to route around this.
func New(apiKey, baseURL, model, effort string, maxTokens int64) (*Generator, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is not set")
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	// One-time record of which configuration this process's Generate calls
	// will use — see theory 1 (reasoning tax) in the diagnostics task this
	// instruments. base_url_set only (not the URL itself) since it's not
	// sensitive but also not needed to answer the question.
	slog.Info("ai: generator configured",
		"model", model,
		"effort", effort,
		"max_tokens", maxTokens,
		"base_url_set", baseURL != "",
		"adaptive_thinking_supported", modelSupportsAdaptiveThinking(model))
	return &Generator{
		client:    anthropic.NewClient(opts...),
		model:     model,
		effort:    anthropic.OutputConfigEffort(effort),
		maxTokens: maxTokens,
	}, nil
}

// NewFake builds a Generator that never calls Claude — Generate instead
// waits fakeDelay (simulating real generation latency, so a caller watching
// the stream WebSocket live still sees a realistic "generating" window
// rather than an instant no-op) and returns a canned, no-op Result: a fixed
// summary, no proposed file changes. That means checkAndRepair's themecheck
// pass is skipped entirely (see proposalHasChanges in themebuild/service.go)
// and nothing is ever written to the real theme — this is for exercising
// the surrounding plumbing (WebSocket delivery, the async generation
// lifecycle, the dashboard's UI) without spending real API tokens while
// that plumbing is still being debugged. Swap back to New(...) once done —
// see config.Config.FakeAIMode / the AI_CHAT_FAKE_MODE env var.
func NewFake(fakeDelay time.Duration) *Generator {
	return &Generator{fake: true, fakeDelay: fakeDelay}
}

// fakeGenerate is NewFake's whole implementation — see its doc comment.
// Deliberately proposes zero file changes: a fixed placeholder path/content
// would risk actually corrupting a real theme (wrong file, wrong shape) the
// moment a caller forgets fake mode is on, and there's no safe way to
// synthesize a real edit without reading the current file first (which
// would need a real tool-use loop, defeating the point of not spending
// tokens). No changes proposed means checkAndRepair's themecheck pass never
// runs and nothing is ever written — this exercises the async generation
// lifecycle and the stream WebSocket end to end, just never the file-write
// path.
func (g *Generator) fakeGenerate(ctx context.Context, prompt string) (*Result, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(g.fakeDelay):
	}
	return &Result{
		Summary: fmt.Sprintf("[fake mode] Received: %q. No changes were made — the AI provider was never called.", prompt),
	}, nil
}

// newTestGenerator builds a Generator pointed at a caller-supplied base URL
// (an httptest.Server standing in for the Anthropic API) — used only by
// this package's own tests, which need to drive the tool loop against a
// fake server rather than the real Claude API.
func newTestGenerator(client anthropic.Client) *Generator {
	return &Generator{client: client, model: "claude-test", effort: anthropic.OutputConfigEffortMedium, maxTokens: defaultMaxTokens}
}

// resultSchema is propose_changes' input_schema (see tools.go) — the same
// JSON Schema Structured Outputs used to enforce directly on the response
// before this package grew a tool loop. additionalProperties: false on
// every object matches the API's schema-validation requirements.
var resultSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"summary", "needs_clarification", "files", "page_registry_entry", "layout_links_to_add", "layout_scripts_to_add"},
	"properties": map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"description": "1-3 plain-language sentences for the merchant-facing chat UI. If needs_clarification is true, this is the clarifying question instead.",
		},
		"needs_clarification": map[string]any{
			"type":        "boolean",
			"description": "true if the request conflicts with a hard rule in the spec or is too ambiguous to safely generate. When true, files must be empty.",
		},
		// Strict: true + additionalProperties: false (see proposeChangesTool)
		// means every property here must be present on every files[] item —
		// content and edits are both always required, their meaning set by
		// action rather than by which one is present. Deliberately not an
		// anyOf/oneOf split keyed on action: DeepSeek's Anthropic-compat
		// endpoint (the actual target for this schema) has unverified
		// support for conditional subschemas, so the contract is documented
		// in each field's description instead and enforced server-side by
		// materializeEdits, not by the schema itself.
		"files": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path", "action", "content", "edits"},
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Theme-root-relative path, e.g. 'pages/offers.liquid'."},
					"action": map[string]any{
						"type": "string", "enum": []string{"create", "update", "edit"},
						"description": "'create'/'update': content is the full file, edits is []. 'edit': content is \"\", " +
							"edits is a non-empty list of find/replace pairs applied to the file's current content.",
					},
					"content": map[string]any{
						"type": "string",
						"description": "Full file content for 'create'/'update' — never a diff or partial snippet. " +
							"\"\" for 'edit', where edits carries the change instead.",
					},
					"edits": map[string]any{
						"type":        "array",
						"description": "Find/replace pairs for action 'edit' — [] for 'create'/'update'. Applied in order.",
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"old_string", "new_string"},
							"properties": map[string]any{
								"old_string": map[string]any{
									"type": "string",
									"description": "Exact text to find. Must match the file's real current content " +
										"exactly once — whitespace and indentation included. Include enough surrounding " +
										"context to make it unique; a single line that repeats elsewhere in the file will " +
										"be rejected.",
								},
								"new_string": map[string]any{
									"type":        "string",
									"description": "Replacement text. Empty string deletes old_string.",
								},
							},
						},
					},
				},
			},
		},
		// page_registry_entry deliberately has no requires_auth property.
		// Per theme_engine_spec.md §5, requires_auth: true only applies to
		// my_account/my_orders/change_password — fixed system route types this
		// service is forbidden from ever (re-)registering. Every page ai-chat
		// can legitimately create is type "custom", which never needs it.
		// Asking the model for a value that's always false, and on top of that
		// silently discarded by flowpos-backend's StoreThemeFileRequest /
		// ThemeFileController today, just invites the model (and future
		// readers) to believe gating works through this path. If a merchant
		// ever needs a gated custom page, that's a spec change plus a
		// flowpos-backend change, decided then — not a field carried
		// speculatively now.
		"page_registry_entry": map[string]any{
			"anyOf": []any{
				map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"title", "slug", "path", "type", "page", "seo_title", "seo_description", "seo_keywords", "og_title", "og_description", "og_image_path", "status"},
					"properties": map[string]any{
						"title":           map[string]any{"type": "string"},
						"slug":            map[string]any{"type": "string"},
						"path":            map[string]any{"type": "string", "enum": []string{"/pages", "/pages/auth"}},
						"type":            map[string]any{"type": "string"},
						"page":            map[string]any{"type": "string"},
						"seo_title":       map[string]any{"type": "string"},
						"seo_description": map[string]any{"type": "string"},
						"seo_keywords":    map[string]any{"type": "string"},
						"og_title":        map[string]any{"type": "string"},
						"og_description":  map[string]any{"type": "string"},
						"og_image_path":   map[string]any{"type": "string"},
						"status":          map[string]any{"type": "string", "enum": []string{"draft", "published"}},
					},
				},
				map[string]any{"type": "null"},
			},
		},
		"layout_links_to_add":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"layout_scripts_to_add": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
}

// modelSupportsAdaptiveThinking reports whether model accepts
// thinking: {type: "adaptive"} and output_config.effort — Haiku-tier models
// reject both with a 400 ("adaptive thinking is not supported on this
// model"), unlike every Opus/Sonnet-tier model this service targets.
func modelSupportsAdaptiveThinking(model anthropic.Model) bool {
	return !strings.Contains(strings.ToLower(model), "haiku")
}

// maxToolIterations bounds the read/explore loop before Generate gives up —
// raised from 8, then from 20: a real page-creation prompt ("design an
// our-story page") was observed spending all 20 iterations on distinct,
// purposeful reads/greps (not stuck looping) and never reaching
// propose_changes, failing the whole generation despite real API cost
// already spent gathering context. See forceProposeAtIteration below for
// the complementary fix — pushing the model to commit near the ceiling
// rather than relying on the ceiling alone to be big enough.
const maxToolIterations = 28

// forceProposeWithinLastN is how close to maxToolIterations the loop gets
// before it stops offering the model a free choice of tool and instead
// forces propose_changes specifically (see the ToolChoice branch in
// Generate) — pushing it to commit to a proposal using whatever context
// it's already gathered, rather than reading indefinitely and running out
// the budget with nothing to show for it.
const forceProposeWithinLastN = 3

// thrashOutputTokenThreshold flags a tool-loop iteration that called only
// read-only exploration tools (list_theme_files/read_theme_file/grep_theme
// — see allExplorationTools) with no propose_changes, yet still burned a
// large amount of output — narration/reasoning the model produced
// alongside tool calls whose own arguments carry almost none of it.
// Observed in production: one iteration of 6 grep_theme calls (whose
// arguments are maybe 200 tokens combined) cost 24,315 output tokens and
// 285 of a 477-second generation's total wall clock — 60% of the run, with
// no file changed. This constant only backs a diagnostic slog.Warn
// (see Generate) so the pattern's real-world frequency can be measured; it
// does not abort, truncate, or otherwise change the loop's behavior.
const thrashOutputTokenThreshold = 5000

// explorationToolNames are the read-only tools a tool-loop iteration can
// call besides propose_changes — see tools.go's toolName* constants.
var explorationToolNames = map[string]bool{
	toolNameListThemeFiles: true,
	toolNameReadThemeFile:  true,
	toolNameGrepTheme:      true,
}

// allExplorationTools reports whether names is non-empty and every entry is
// a read-only exploration tool — i.e. this iteration explored but never
// called propose_changes.
func allExplorationTools(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if !explorationToolNames[n] {
			return false
		}
	}
	return true
}

// streamAccumulateMaxAttempts is how many times a single tool-loop
// iteration's streaming call is attempted when the provider's stream itself
// arrives truncated/garbled mid-chunk (see isRetryableAccumulateErr) —
// observed in production as a transport-level hiccup unrelated to the
// request, so a short retry resolves it in practice rather than failing the
// whole generation. streamAccumulateRetryDelay between attempts gives the
// provider a moment to recover; 3 attempts * 5s = up to 10s of retrying
// before giving up.
const streamAccumulateMaxAttempts = 3
const streamAccumulateRetryDelay = 5 * time.Second

// isRetryableAccumulateErr reports whether err is message.Accumulate failing
// to parse a truncated/garbled streamed chunk — see sanitize.go's matching
// categorization of the same error text for what the merchant sees if every
// retry still fails.
func isRetryableAccumulateErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "accumulate stream")
}

// defaultMaxTokens is used when ANTHROPIC_MAX_TOKENS is unset — comfortably
// below Opus-tier's real ceiling (per Anthropic's docs, 64000 is nowhere
// near the effective context/output limit for claude-opus-* models) while
// still well above the prior fixed 32000, which was observed truncating
// large page-generation proposals mid-JSON.
const defaultMaxTokens = 64000

// errMaxTokensTruncated is returned instead of attempting to json.Unmarshal
// a propose_changes input that Claude's own StopReason says was cut off
// mid-stream — unmarshaling truncated JSON either errors confusingly or,
// worse, could succeed on a coincidentally-valid prefix and silently accept
// a partial proposal.
var errMaxTokensTruncated = errors.New("model response was truncated at the max_tokens limit before propose_changes could be parsed")

// Generate asks Claude for the file changes implementing prompt, given the
// theme context and prior conversation turns. The model drives a tool loop:
// each call may return one or more tool_use blocks, which toolExec executes
// (list_theme_files/read_theme_file/grep_theme — ai never touches themefs
// itself, see ToolExecutor), with the results fed back as a new turn, until
// the model calls propose_changes, whose input becomes Result — with one
// extra step first: any "edit"-action file is materialized into "update"
// via materializeEdits(readFile) before the result is returned, so callers
// never see "edit" (see GeneratedFile's doc comment). A materialization
// failure does NOT return an error or end the turn: it's fed back as this
// propose_changes call's own tool_result, and the loop continues exactly
// like an ordinary tool call would, giving the model a chance to correct
// itself — see the propose_changes handling below. onDelta, if non-nil, is
// called with each new chunk of raw text the model streams (thinking-style
// narration, not the proposal itself) across every iteration — mainly
// useful for a live "..." progress indicator. progress, if non-nil, is
// notified around every toolExec call — see ToolProgress's doc comment for
// why the caller (not this method) decides what to do with that.
func (g *Generator) Generate(ctx context.Context, tc ThemeContext, history []Turn, prompt string, onDelta func(string), progress ToolProgress, toolExec ToolExecutor, readFile FileReader) (*Result, error) {
	if g.fake {
		return g.fakeGenerate(ctx, prompt)
	}
	// Keyed by path, persists across every iteration of this one Generate
	// call — see materializeEdits' own doc comment on why a path that keeps
	// failing needs to fall back to requesting full content rather than
	// retrying forever.
	editFailureCounts := make(map[string]int)
	// Anthropic rejects any empty text content block outright ("text content
	// blocks must be non-empty") — not just for the cache_control
	// breakpoint below, for any message anywhere in the request — so an
	// empty turn is skipped rather than replayed. The caller (themebuild)
	// already filters these out of history itself; this is defense in depth
	// for any other caller of Generate, present or future.
	messages := make([]anthropic.MessageParam, 0, len(history)+1)
	for _, t := range history {
		if strings.TrimSpace(t.Content) == "" {
			continue
		}
		if strings.EqualFold(t.Role, "assistant") {
			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(t.Content)))
		} else {
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(t.Content)))
		}
	}
	// Mark the end of the replayed history as a cache breakpoint: on a chat's
	// 2nd+ turn, everything up to (and including) this block is byte-identical
	// to the previous call, so Anthropic serves it from cache instead of
	// reprocessing the whole conversation-so-far on every single message. Only
	// the new prompt below (appended without cache_control) is genuinely new.
	// The empty-text filter above already guarantees this block is
	// non-empty; the OfText nil-check here is just defensive.
	if last := len(messages) - 1; last >= 0 {
		lastBlock := messages[last].Content[len(messages[last].Content)-1]
		if lastBlock.OfText != nil && lastBlock.OfText.Text != "" {
			cacheControl := anthropic.NewCacheControlEphemeralParam()
			cacheControl.TTL = anthropic.CacheControlEphemeralTTLTTL1h
			lastBlock.OfText.CacheControl = cacheControl
		}
	}
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))

	tools := toolsForMode(tc.GenerationMode)
	// The dynamic block (pages.json, defaults.json, file tree, manifest —
	// often several thousand tokens on a theme with many pages) is
	// byte-identical across every iteration of THIS call's tool loop and
	// every checkAndRepair retry that reuses the same tc — only the
	// replayed history and the new prompt/repair message change between
	// those. Without its own breakpoint it was being resent and
	// reprocessed at full price on every single one of those calls; this
	// makes iteration 2+ of the same turn (and any retry) read it from
	// cache instead (~10% of the cost). Safe to always set: prompt caching
	// requires a minimum prefix length (512-4096 tokens depending on
	// model) below which this silently doesn't cache rather than erroring.
	dynamicBlock := anthropic.TextBlockParam{Text: dynamicSystemPrompt(tc)}
	dynamicCacheControl := anthropic.NewCacheControlEphemeralParam()
	dynamicCacheControl.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	dynamicBlock.CacheControl = dynamicCacheControl
	system := []anthropic.TextBlockParam{staticSystemPromptBlock(), dynamicBlock}

	var totalInputTokens, totalOutputTokens int64
	// generateStart/totalModelElapsed/totalToolElapsed/iterationsUsed back
	// the single summary line the deferred log below emits on every return
	// path (success or error) — see theories 1-4 in the diagnostics task
	// this instruments; none of these affect control flow.
	generateStart := time.Now()
	var totalModelElapsed, totalToolElapsed time.Duration
	iterationsUsed := 0
	// totalReasoningTokens/reasoningTokensReported back theory 1 (reasoning
	// tax) directly — see the per-iteration "ai: model call timing" log
	// below for what sets reasoningTokensReported and why a false there
	// means "not reported by this provider," not "confirmed zero."
	var totalReasoningTokens int64
	reasoningTokensReported := false
	defer func() {
		slog.Info("ai: generate call finished",
			"iterations_used", iterationsUsed,
			"elapsed_ms", time.Since(generateStart).Milliseconds(),
			"model_elapsed_ms", totalModelElapsed.Milliseconds(),
			"tool_elapsed_ms", totalToolElapsed.Milliseconds(),
			"total_input_tokens", totalInputTokens,
			"total_output_tokens", totalOutputTokens,
			"total_reasoning_tokens", totalReasoningTokens,
			"reasoning_tokens_reported", reasoningTokensReported)
	}()
	for iteration := 0; iteration < maxToolIterations; iteration++ {
		iterationsUsed = iteration + 1
		toolChoice := anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
		forcingPropose := iteration >= maxToolIterations-forceProposeWithinLastN
		if forcingPropose {
			// Near the ceiling: stop offering read/explore tools as an equally
			// valid choice and force propose_changes specifically, so the model
			// commits to a proposal from whatever it's already gathered instead
			// of spending its last few iterations reading more and running out
			// the clock with nothing produced. If it still doesn't call
			// propose_changes (calls something else anyway, or errors), the loop
			// falls through to the usual "did not call propose_changes" failure
			// below — no special handling needed beyond making this attempt happen.
			toolChoice = anthropic.ToolChoiceParamOfTool(toolNameProposeChanges)
			slog.Info("ai: forcing propose_changes near tool-loop budget ceiling",
				"iteration", iteration, "max_tool_iterations", maxToolIterations)
		}
		params := anthropic.MessageNewParams{
			Model:      g.model,
			MaxTokens:  g.maxTokens,
			System:     system,
			Messages:   messages,
			Tools:      tools,
			ToolChoice: toolChoice,
		}
		if forcingPropose {
			// Being forced to call propose_changes does not mean the model has
			// a real, finished proposal — every field in a files[] entry is
			// schema-required, and without this nudge a model forced to commit
			// before it's ready has been observed inventing a stand-in path
			// (e.g. "__placeholder__", no extension) rather than admitting it
			// isn't done, which then fails ValidateGeneratedFilePath downstream.
			// needs_clarification + an empty files array is already a valid,
			// schema-legal way to say "not ready" — this just tells the model
			// that escape hatch exists and should be used here instead of
			// fabricating file content.
			params.System = append(append([]anthropic.TextBlockParam{}, system...), anthropic.TextBlockParam{
				Text: "You are at the tool-loop budget ceiling and must call propose_changes now. " +
					"Only include a file in `files` if you actually read/verified its current content " +
					"(for an update) or have real, complete content ready (for a create) — never invent " +
					"a placeholder path or partial content to fill the array. If you do not yet have a " +
					"complete, verified proposal, call propose_changes with needs_clarification: true, " +
					"files: [], and a summary explaining that the request needs to be split into a " +
					"smaller step or retried, instead of guessing.",
			})
		}
		// Adaptive thinking and output_config.effort are both rejected outright
		// (400) on Haiku-tier models — leave both fields zero-valued (omitted
		// from the request, see their "omitzero" json tags) rather than
		// sending a value that model can't accept.
		if modelSupportsAdaptiveThinking(g.model) {
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
			params.OutputConfig = anthropic.OutputConfigParam{Effort: g.effort}
		}
		var message anthropic.Message
		// modelCallStart/attemptsUsed cover every streamAccumulateMaxAttempts
		// retry within this one iteration — a slow iteration due to a
		// retried stream isn't misread as slow inference, since
		// attempts_used is logged alongside elapsed_ms below.
		modelCallStart := time.Now()
		attemptsUsed := 0
		for attempt := 1; attempt <= streamAccumulateMaxAttempts; attempt++ {
			attemptsUsed = attempt
			stream := g.client.Messages.NewStreaming(ctx, params)
			message = anthropic.Message{}
			emitted := 0
			var accumulateErr error
			// Fresh per attempt, same as message/emitted above: a retried
			// attempt is its own streaming API call with its own text run,
			// and coalescer.flush() below already empties it before the next
			// attempt/iteration could otherwise reuse a stale buffer. Note
			// this does mean a retry re-emits onDelta from the start of this
			// iteration's text — acceptable since the failure this retries
			// (see isRetryableAccumulateErr) happens while parsing a
			// tool_use block's arguments, which iterations that narrate
			// meaningful text rarely reach before it would have failed.
			coalescer := newDeltaCoalescer(onDelta)
			for stream.Next() {
				if err := message.Accumulate(stream.Current()); err != nil {
					accumulateErr = fmt.Errorf("accumulate stream: %w", err)
					break
				}
				if full := currentText(message); len(full) > emitted {
					coalescer.add(full[emitted:])
					emitted = len(full)
				}
			}
			// Whatever's still buffered when this attempt ends must go out
			// now — otherwise the last <80-char, <200ms fragment of a turn's
			// narration (very often the tail end, since a stream just ending
			// is exactly when there's no more input to trigger the next
			// add() call that would have flushed it) is silently lost.
			coalescer.flush()
			if accumulateErr == nil {
				if err := stream.Err(); err != nil {
					return nil, fmt.Errorf("claude stream: %w", err)
				}
				break
			}
			if !isRetryableAccumulateErr(accumulateErr) || attempt == streamAccumulateMaxAttempts {
				return nil, accumulateErr
			}
			// The provider's stream arrived truncated/garbled mid-chunk — a
			// transport-level hiccup, not anything about this request — so a
			// short pause and a fresh attempt resolves it in practice.
			slog.Warn("ai: provider stream truncated/garbled, retrying", "attempt", attempt,
				"max_attempts", streamAccumulateMaxAttempts, "error", accumulateErr.Error())
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(streamAccumulateRetryDelay):
			}
		}
		modelElapsed := time.Since(modelCallStart)
		totalModelElapsed += modelElapsed
		totalInputTokens += message.Usage.InputTokens
		totalOutputTokens += message.Usage.OutputTokens
		// OutputTokensDetails is a plain value struct (never nil), and
		// ThinkingTokens a plain int64 — no pointer to guard. Whether the
		// provider actually populated it is instead tracked by the SDK's own
		// presence marker (respjson.Field.Valid, same mechanism used for
		// every other optional field on Usage): reasoningTokensValid is
		// false when DeepSeek's response omitted output_tokens_details (or
		// its thinking_tokens) entirely, distinguishing that from a
		// genuinely-reported 0.
		reasoningTokens := message.Usage.OutputTokensDetails.ThinkingTokens
		reasoningTokensValid := message.Usage.OutputTokensDetails.JSON.ThinkingTokens.Valid()
		totalReasoningTokens += reasoningTokens
		if reasoningTokensValid {
			reasoningTokensReported = true
		}

		var toolUses []anthropic.ContentBlockUnion
		var proposeInput json.RawMessage
		// textBlockCount/textChars are counted here, off the fully
		// accumulated message.Content for this iteration (after the
		// attempt loop above has already finished reassembling the whole
		// streamed response) — the same source toolUses/toolNames below
		// already reads, not raw incremental SSE deltas, so a still-in-
		// progress or retried attempt is never double-counted.
		textBlockCount := 0
		textChars := 0
		for _, block := range message.Content {
			if block.Type == "text" {
				textBlockCount++
				textChars += len(block.Text)
				continue
			}
			if block.Type != "tool_use" {
				continue
			}
			toolUses = append(toolUses, block)
			if block.Name == toolNameProposeChanges {
				proposeInput = block.Input
			}
		}

		toolNames := make([]string, len(toolUses))
		for i, tu := range toolUses {
			toolNames[i] = tu.Name
		}
		slog.Info("ai: tool-loop iteration", "iteration", iteration, "tools_called", toolNames, "stop_reason", message.StopReason)
		// Model-call latency/token breakdown for this iteration only — see
		// theories 1 (reasoning tax) and 2 (prompt caching) in the
		// diagnostics task this instruments. cache_read_input_tokens > 0 on
		// iteration 2+ means caching is actually working (whether or not
		// Anthropic's cache_control is what triggered it).
		slog.Info("ai: model call timing",
			"iteration", iteration,
			"elapsed_ms", modelElapsed.Milliseconds(),
			"attempts_used", attemptsUsed,
			"forcing_propose", forcingPropose,
			"input_tokens", message.Usage.InputTokens,
			"output_tokens", message.Usage.OutputTokens,
			"cache_read_input_tokens", message.Usage.CacheReadInputTokens,
			"cache_creation_input_tokens", message.Usage.CacheCreationInputTokens,
			"reasoning_tokens", reasoningTokens,
			"reasoning_tokens_reported", reasoningTokensValid,
			"text_block_count", textBlockCount,
			"text_chars", textChars,
			"tool_use_count", len(toolUses))

		// Flags, never controls: an iteration that called only read-only
		// exploration tools (no propose_changes) yet still burned a large
		// amount of output is the "thrash" pattern observed in production
		// — see thrashOutputTokenThreshold's own doc comment for the
		// 24,315-token/6-grep-call case that motivated this. Purely
		// diagnostic — nothing about the loop's own behavior changes here.
		if allExplorationTools(toolNames) && message.Usage.OutputTokens > thrashOutputTokenThreshold {
			slog.Warn("ai: tool-loop iteration spent unusually many output tokens on exploration only",
				"iteration", iteration, "output_tokens", message.Usage.OutputTokens, "tools_called", toolNames)
		}

		// StopReason == "max_tokens" means Claude was cut off mid-stream —
		// propose_changes' input (if any tool_use block even parsed as valid
		// JSON that far) is truncated, not a real proposal. Unmarshaling it
		// anyway either fails confusingly or, worse, could succeed against a
		// coincidentally well-formed prefix and silently accept a partial
		// result — fail explicitly instead.
		if message.StopReason == anthropic.StopReasonMaxTokens {
			return nil, errMaxTokensTruncated
		}

		// materializeFailureMsg, when non-empty, is fed back below as the
		// propose_changes tool_use's own tool_result (isError: true) instead
		// of returning — see materializeEdits' doc comment. Declared here
		// (not inside the if) so the general toolUses loop further down can
		// see it regardless of which branch set it.
		var materializeFailureMsg string
		if proposeInput != nil {
			var result Result
			if err := json.Unmarshal(proposeInput, &result); err != nil {
				return nil, fmt.Errorf("could not parse propose_changes input: %w", err)
			}
			ok, retryMsg := materializeEdits(ctx, &result, readFile, editFailureCounts)
			if ok {
				result.InputTokens = totalInputTokens
				result.OutputTokens = totalOutputTokens
				return &result, nil
			}
			slog.Warn("ai: propose_changes edit materialization failed, retrying", "iteration", iteration)
			materializeFailureMsg = retryMsg
		}

		if len(toolUses) == 0 {
			// Counts how often this nudge fires — see theory 3 (wasted
			// round-trips) in the diagnostics task this instruments.
			slog.Warn("ai: tool-loop nudge fired (zero tool calls despite forced tool_choice)", "iteration", iteration)
			// Real Anthropic's ToolChoice: OfAny guarantees at least one tool
			// call. DeepSeek's Anthropic-compat endpoint does NOT honor that
			// guarantee — confirmed empirically: a plain "hello"/"hi" gets a
			// conversational end_turn reply with zero tool calls despite
			// tool_choice being forced, because the model reasons (visibly,
			// in its own thinking block) that no tool is needed. Failing the
			// whole generation over that would turn every greeting into an
			// error. Nudge instead: replay the model's own toolless turn,
			// tell it a tool call is required, and let the loop's own
			// budget (maxToolIterations, forcingPropose near the ceiling)
			// bound how long this can go on — same safety net as the normal
			// tool-call path below, just without a real tool_result to reply
			// with.
			messages = append(messages, message.ToParam())
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(
				"You must call one of the available tools on every turn — propose_changes if you already have enough "+
					"to finish (even for a simple greeting or question, propose_changes with no file changes and a "+
					"reply in `summary` is correct), or a read/explore tool otherwise. A plain text reply with no tool "+
					"call is not valid here.",
			)))
			continue
		}

		// Replay the model's own turn (its narration text plus every
		// tool_use block, verbatim) before the tool_result turn that
		// answers it — required so the next call has the tool_use ids the
		// results below reference, and (with adaptive thinking on) so any
		// thinking block that preceded the tool calls is preserved.
		messages = append(messages, message.ToParam())

		resultBlocks := make([]anthropic.ContentBlockParamUnion, 0, len(toolUses))
		for _, tu := range toolUses {
			// propose_changes was already "executed" above (parsed, edits
			// materialized) — reaching this loop for it at all means that
			// failed, so its tool_result is the failure description rather
			// than a real toolExec call (propose_changes isn't one of
			// ToolExecutor's tools; calling toolExec with it would just
			// error "unknown tool").
			if tu.Name == toolNameProposeChanges {
				resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(tu.ID, materializeFailureMsg, true))
				continue
			}
			if progress != nil {
				progress.ToolStarted(tu.Name, tu.Input)
			}
			toolCallStart := time.Now()
			output, err := toolExec(ctx, tu.Name, tu.Input)
			toolElapsed := time.Since(toolCallStart)
			totalToolElapsed += toolElapsed
			// Distinguishes tool-execution latency (an HTTP round trip to
			// FlowPOS) from model latency logged above — see "ai: model call
			// timing".
			slog.Info("ai: tool exec timing", "iteration", iteration, "tool", tu.Name, "elapsed_ms", toolElapsed.Milliseconds(), "error", err != nil)
			isError := err != nil
			if progress != nil {
				// Summarized from output/err before output is overwritten
				// below with err.Error() — summarizeToolResult wants the
				// real error, not the string it gets turned into for the
				// model's own tool_result block.
				progress.ToolFinished(tu.Name, summarizeToolResult(tu.Name, output, err), err)
			}
			if err != nil {
				output = err.Error()
			}
			resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(tu.ID, output, isError))
		}
		messages = append(messages, anthropic.NewUserMessage(resultBlocks...))
	}

	return nil, fmt.Errorf("model did not call propose_changes within %d tool-loop iterations", maxToolIterations)
}

// summarizeMaxTokens caps the summary completion — this is a cheap plain-
// text call (no tools, no thinking), so it needs nowhere near g.maxTokens;
// a few paragraphs of prose fits comfortably within this.
const summarizeMaxTokens = 1024

// Summarize asks Claude for a concise prose summary of turns, framed as
// prior context for continuing the same theme-editing conversation — used
// by themebuild's history-summarization pass (see service.go's
// summarizeOldTurns) to collapse old turns into one synthetic turn instead
// of resending them verbatim on every call. No tools, no thinking, no
// system prompt beyond the instruction below — this is a plain completion,
// not a generation call, so it doesn't touch toolsForMode/dynamicSystemPrompt
// at all.
func (g *Generator) Summarize(ctx context.Context, turns []Turn) (string, error) {
	if g.fake {
		return fmt.Sprintf("[fake mode summary of %d turns]", len(turns)), nil
	}

	var transcript strings.Builder
	for _, t := range turns {
		if strings.TrimSpace(t.Content) == "" {
			continue
		}
		fmt.Fprintf(&transcript, "%s: %s\n\n", strings.ToUpper(t.Role), t.Content)
	}

	instruction := "Summarize the conversation below in a few short paragraphs (no more than a few paragraphs " +
		"total). This is prior context for continuing the SAME theme-editing conversation — write it as a working " +
		"summary an assistant could use to keep working coherently, not a transcript. Cover what was discussed, " +
		"what was built or changed, and any decisions made. Do not include a preamble or restate this instruction.\n\n" +
		"<conversation>\n" + transcript.String() + "</conversation>"

	params := anthropic.MessageNewParams{
		Model:     g.model,
		MaxTokens: summarizeMaxTokens,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(instruction))},
	}
	message, err := g.client.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("summarize turns: %w", err)
	}
	return currentText(*message), nil
}

// staticSystemPromptBlock is the theme-engine spec plus the fixed generation
// rules — byte-identical on every single call, regardless of tenant, theme,
// or request. It carries a 1h cache_control breakpoint so Anthropic's prompt
// cache serves it on every call after the first instead of Claude
// re-processing the full spec (and paying full input-token price for it) on
// every single message.
func staticSystemPromptBlock() anthropic.TextBlockParam {
	cacheControl := anthropic.NewCacheControlEphemeralParam()
	cacheControl.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	return anthropic.TextBlockParam{
		Text: fmt.Sprintf(`You generate Liquid theme code for the flowPOS storefront platform. You strictly
follow the theme engine convention below — never Shopify's real theme conventions, never a
different templating language, never a UI framework. This is a proprietary, simplified Liquid
dialect described in full below.

<theme_engine_spec>
%s
</theme_engine_spec>

Rules for every request:
1. Read the merchant's request. If it asks to create/modify a page, compose it from existing
   components listed in the spec wherever one fits; only write new component/page markup for
   what doesn't already exist.
2. Follow every rule in the spec's "Hard rules" section without exception. If a request conflicts
   with a hard rule (e.g. asks for a data field that doesn't exist, or a JS framework), do not
   silently comply — set needs_clarification and explain the conflict in summary instead of guessing.
3. Output ONLY the files that changed or were created. Do not re-emit unchanged files.
4. Every new pages/*.liquid file must include the exact layout-start/layout-end boilerplate from
   the spec.
5. Every new page needs a pages.json entry (return it in page_registry_entry); every new CSS/JS
   file needs its <link>/<script> tag registered (return those paths in layout_links_to_add /
   layout_scripts_to_add).
6. Keep changes scoped to the request — don't refactor components you weren't asked to touch,
   don't add sections the merchant didn't ask for, don't add narrating code comments.
7. summary is shown directly to the merchant in a chat UI: 1-3 plain-language sentences, no code,
   no file paths, describing what you built. If you set needs_clarification, summary is your
   question to the merchant instead.
8. Before you modify any existing file, read it with read_theme_file — never write a file you
   have not read, and never guess at its current content. Emit only files whose content actually
   changes as a result of this request.
9. Use list_theme_files/read_theme_file/grep_theme as needed to explore the theme before you
   finalize anything. Call propose_changes exactly once, when you're done, with the complete,
   final set of changes for this request — not a partial draft.`, themeEngineSpec),
		CacheControl: cacheControl,
	}
}

// dynamicSystemPrompt is the per-request grounding that varies on every call
// — which theme, its current route registry, its brand defaults, its file
// tree, and this turn's mode restriction — so it carries no cache_control
// (see staticSystemPromptBlock for the part that does).
func dynamicSystemPrompt(tc ThemeContext) string {
	pagesJSON := tc.PagesJSON
	if pagesJSON == "" {
		pagesJSON = "[]"
	}
	defaultsJSON := tc.DefaultsJSON
	if defaultsJSON == "" {
		defaultsJSON = "{}"
	}
	mode := tc.GenerationMode
	if mode == "" {
		mode = GenerationModeEdit
	}

	return fmt.Sprintf(`## Theme being edited
- Theme slug: %s
- MODE: %s%s
- Current pages.json (existing routes — never register a slug that's already here):
%s
- Current defaults.json (brand colors, fonts, menu, footer — match this, don't invent a different palette):
%s
- Current file tree (call list_theme_files again if this feels stale):
%s
%s`, tc.ThemeSlug, mode, modeRestrictionNote(mode), pagesJSON, defaultsJSON, formatFileTree(tc.FileTree), formatManifest(tc.Manifest))
}

// formatManifest renders the manifest's component param index, if one was
// supplied — "" (nothing appended) when it wasn't, rather than a
// placeholder section that would just say "no manifest available".
func formatManifest(m *themefs.Manifest) string {
	if m == nil || len(m.Components) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("- Existing components/partials and their inferred params (pass these explicitly when you render one; read_theme_file it first if a param's purpose isn't obvious from its name):\n")
	for _, c := range m.Components {
		fmt.Fprintf(&b, "  - %s(%s)\n", c.Path, strings.Join(c.Params, ", "))
	}
	return b.String()
}

// modeRestrictionNote spells out MODE's restriction inline — a bare "MODE:
// brand" line assumes the model already knows what that means, which it
// doesn't have any other way to learn.
func modeRestrictionNote(mode string) string {
	switch mode {
	case GenerationModeBrand:
		return " — this turn may ONLY change defaults.json (brand colors/fonts/layout tokens). Do not propose any .liquid/.css/.js file."
	case GenerationModeCopy:
		return " — this turn may only edit hardcoded text/copy inside existing components and pages. Do not add pages, components, or change structure/markup beyond the text itself."
	case GenerationModePages:
		return " — this turn is for adding new pages composed from existing components. Do not edit defaults.json or existing components' markup."
	default:
		return ""
	}
}

// formatFileTree renders a theme's file tree as an indented plain-text
// listing — compact and readable for the model, not JSON, since this rides
// in the system prompt on every call rather than a one-off tool result.
func formatFileTree(entries []themefs.FileTreeEntry) string {
	if len(entries) == 0 {
		return "(empty)"
	}
	var b strings.Builder
	writeFileTree(&b, entries, 0)
	return strings.TrimRight(b.String(), "\n")
}

func writeFileTree(b *strings.Builder, entries []themefs.FileTreeEntry, depth int) {
	for _, e := range entries {
		fmt.Fprintf(b, "%s%s\n", strings.Repeat("  ", depth), e.Name)
		if len(e.Children) > 0 {
			writeFileTree(b, e.Children, depth+1)
		}
	}
}

// coalesceInterval/coalesceMaxChars bound how often onDelta actually fires
// — Anthropic streams text token-by-token (often single words or even
// sub-word pieces per SSE frame), and a caller wired to publish each delta
// live (see themebuild's EventTypeThinking) would otherwise put one Redis
// publish and one WebSocket write per token. Flushing on whichever limit
// is hit first keeps narration feeling live (never more than ~200ms stale)
// without the per-token volume.
const (
	coalesceInterval = 200 * time.Millisecond
	coalesceMaxChars = 80
)

// deltaCoalescer batches onDelta's raw per-token chunks into fewer, larger
// calls — see coalesceInterval/coalesceMaxChars. Not safe for concurrent
// use; Generate only ever calls add/flush from its own single goroutine's
// streaming loop, so it doesn't need to be.
type deltaCoalescer struct {
	onDelta   func(string)
	buf       strings.Builder
	lastFlush time.Time
}

func newDeltaCoalescer(onDelta func(string)) *deltaCoalescer {
	return &deltaCoalescer{onDelta: onDelta, lastFlush: time.Now()}
}

// add appends chunk to the buffer and flushes immediately if either limit
// is already hit — a no-op entirely if onDelta is nil (the common case:
// most callers, e.g. Summarize's plain completion, never stream progress
// at all), so add's caller doesn't need its own nil check before calling it.
func (c *deltaCoalescer) add(chunk string) {
	if c.onDelta == nil {
		return
	}
	c.buf.WriteString(chunk)
	if c.buf.Len() >= coalesceMaxChars || time.Since(c.lastFlush) >= coalesceInterval {
		c.flush()
	}
}

// flush sends whatever's buffered (a no-op if nothing is) — called both
// from add(), on either limit, and once more by Generate right after each
// streaming call ends, so a short final fragment that never hit either
// limit on its own still goes out instead of being silently dropped.
func (c *deltaCoalescer) flush() {
	if c.buf.Len() == 0 {
		return
	}
	text := c.buf.String()
	c.buf.Reset()
	c.lastFlush = time.Now()
	c.onDelta(text)
}

// currentText concatenates the accumulated text and thinking blocks of a
// (possibly partial) message — the model's narration, not the proposal
// itself, which arrives as a tool call's input rather than text (see
// Generate). Thinking blocks matter here specifically because adaptive
// thinking is enabled (modelSupportsAdaptiveThinking) and ToolChoiceAny
// forces a tool call on every single iteration (see Generate's own doc
// comment) — a model given no free choice of "just reply with text" often
// emits little or no plain TextBlock content, so most of a turn's actual
// narration lives in ThinkingBlock instead. Without reading it too,
// onDelta's caller (see themebuild's EventTypeThinking wiring) would see
// almost nothing on most turns.
//
// block.AsAny() returns `any` — an unrecognized block shape (a future SDK
// addition, or a provider-specific variant via DeepSeek's Anthropic-compat
// endpoint, see New's doc comment) simply matches neither switch case below
// and is skipped, never a panic. This path is provider-sensitive: DeepSeek
// may serialize thinking differently than Anthropic's own API, and
// skip-not-crash is the deliberate, defensive choice for that uncertainty
// rather than assuming every provider's blocks look identical.
func currentText(message anthropic.Message) string {
	var text strings.Builder
	for _, block := range message.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			text.WriteString(b.Text)
		case anthropic.ThinkingBlock:
			text.WriteString(b.Thinking)
		}
	}
	return text.String()
}

// maxToolResultSummaryChars bounds ToolProgress.ToolFinished's summary — it
// lands in a persisted event payload downstream (see themebuild's
// AppendGenerationEvent), which is why this stays well under the ~40
// character budget the caller asked for even once a short prefix like
// "failed: " is added.
const maxToolResultSummaryChars = 40

// summarizeToolResult turns one tool call's raw output into the short,
// human-readable result ToolFinished reports — a line count for a read, a
// match count for a grep, an entry count for a list. Computed here (not by
// ToolProgress's implementation) because this package already owns each
// toolName*'s shape via its own tool definitions (tools.go) and has the raw
// output/error in hand right where the call happens; every ToolProgress
// implementation re-deriving "3 matches" from a raw grep_theme string would
// just be this same logic duplicated at each call site. These are
// approximations read off the tool's own output text, not a fresh
// recomputation against the real theme (Generate never touches themefs
// directly — see ToolExecutor's doc comment) — good enough for "what just
// happened" narration, not meant to be authoritative.
func summarizeToolResult(name, output string, toolErr error) string {
	if toolErr != nil {
		return truncateSummary("failed: "+toolErr.Error(), maxToolResultSummaryChars)
	}
	switch name {
	case toolNameListThemeFiles:
		// output is the JSON-encoded file tree (see themebuild's
		// execListThemeFiles) — counting `"path":` occurrences approximates
		// the file+directory count without this package decoding the tree
		// itself, which would mean depending on themefs.FileTreeEntry's
		// exact shape for a display nicety alone.
		return fmt.Sprintf("%d entries", strings.Count(output, `"path":`))
	case toolNameReadThemeFile:
		return fmt.Sprintf("%d lines", strings.Count(output, "\n"))
	case toolNameGrepTheme:
		return summarizeGrepResult(output)
	default:
		return truncateSummary(output, maxToolResultSummaryChars)
	}
}

// summarizeGrepResult counts execGrepTheme's own match lines ("path:line:
// text", one per match) in its raw output — everything except the special
// "(no matches)" result and an optional trailing "(stopped at N matches…)"
// note, both of which start with "(" and aren't matches themselves.
func summarizeGrepResult(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "(no matches)" {
		return "no matches"
	}
	matches := 0
	for _, line := range strings.Split(trimmed, "\n") {
		if line != "" && !strings.HasPrefix(line, "(") {
			matches++
		}
	}
	return fmt.Sprintf("%d match(es)", matches)
}

// truncateSummary bounds s to max runes, collapsing newlines to spaces
// first — a summary is meant to be a single display line.
func truncateSummary(s string, max int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}
