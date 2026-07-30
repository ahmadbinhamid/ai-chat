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
	"fmt"
	"strings"

	"ai-chat/internal/themefs"

	"github.com/anthropics/anthropic-sdk-go"
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

// GeneratedFile is one file the model proposes creating or updating.
type GeneratedFile struct {
	Path    string `json:"path"`
	Action  string `json:"action"` // "create" | "update"
	Content string `json:"content"`
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
}

// New constructs the Claude client. apiKey empty is a configuration error
// the caller should surface at startup (this service has no "run without AI"
// mode — generation is the entire product), not something to silently
// degrade around.
func New(apiKey, model, effort string) (*Generator, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}
	return &Generator{
		client: anthropic.NewClient(),
		model:  anthropic.Model(model),
		effort: anthropic.OutputConfigEffort(effort),
	}, nil
}

// newTestGenerator builds a Generator pointed at a caller-supplied base URL
// (an httptest.Server standing in for the Anthropic API) — used only by
// this package's own tests, which need to drive the tool loop against a
// fake server rather than the real Claude API.
func newTestGenerator(client anthropic.Client, model string) *Generator {
	return &Generator{client: client, model: model, effort: anthropic.OutputConfigEffortMedium}
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
		"files": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path", "action", "content"},
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Theme-root-relative path, e.g. 'pages/offers.liquid'."},
					"action":  map[string]any{"type": "string", "enum": []string{"create", "update"}},
					"content": map[string]any{"type": "string", "description": "Full file content — never a diff or partial snippet."},
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
	return !strings.Contains(strings.ToLower(string(model)), "haiku")
}

// maxToolIterations bounds the read/explore loop before Generate gives up —
// generous enough for "list, read three files, grep once, propose" with
// room to spare, tight enough that a model stuck re-reading the same file
// fails fast instead of burning the caller's timeout budget.
const maxToolIterations = 8

// Generate asks Claude for the file changes implementing prompt, given the
// theme context and prior conversation turns. The model drives a tool loop:
// each call may return one or more tool_use blocks, which toolExec executes
// (list_theme_files/read_theme_file/grep_theme — ai never touches themefs
// itself, see ToolExecutor), with the results fed back as a new turn, until
// the model calls propose_changes, whose input becomes Result. onDelta, if
// non-nil, is called with each new chunk of raw text the model streams
// (thinking-style narration, not the proposal itself) across every
// iteration — mainly useful for a live "..." progress indicator.
func (g *Generator) Generate(ctx context.Context, tc ThemeContext, history []Turn, prompt string, onDelta func(string), toolExec ToolExecutor) (*Result, error) {
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
	system := []anthropic.TextBlockParam{staticSystemPromptBlock(), {Text: dynamicSystemPrompt(tc)}}

	var totalInputTokens, totalOutputTokens int64
	for iteration := 0; iteration < maxToolIterations; iteration++ {
		params := anthropic.MessageNewParams{
			Model:      g.model,
			MaxTokens:  32000,
			System:     system,
			Messages:   messages,
			Tools:      tools,
			ToolChoice: anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}},
		}
		// Adaptive thinking and output_config.effort are both rejected outright
		// (400) on Haiku-tier models — leave both fields zero-valued (omitted
		// from the request, see their "omitzero" json tags) rather than
		// sending a value that model can't accept.
		if modelSupportsAdaptiveThinking(g.model) {
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
			params.OutputConfig = anthropic.OutputConfigParam{Effort: g.effort}
		}
		stream := g.client.Messages.NewStreaming(ctx, params)

		message := anthropic.Message{}
		emitted := 0
		for stream.Next() {
			if err := message.Accumulate(stream.Current()); err != nil {
				return nil, fmt.Errorf("accumulate stream: %w", err)
			}
			if onDelta != nil {
				if full := currentText(message); len(full) > emitted {
					onDelta(full[emitted:])
					emitted = len(full)
				}
			}
		}
		if err := stream.Err(); err != nil {
			return nil, fmt.Errorf("claude stream: %w", err)
		}
		totalInputTokens += message.Usage.InputTokens
		totalOutputTokens += message.Usage.OutputTokens

		var toolUses []anthropic.ContentBlockUnion
		var proposeInput json.RawMessage
		for _, block := range message.Content {
			if block.Type != "tool_use" {
				continue
			}
			toolUses = append(toolUses, block)
			if block.Name == toolNameProposeChanges {
				proposeInput = block.Input
			}
		}

		if proposeInput != nil {
			var result Result
			if err := json.Unmarshal(proposeInput, &result); err != nil {
				return nil, fmt.Errorf("could not parse propose_changes input: %w", err)
			}
			result.InputTokens = totalInputTokens
			result.OutputTokens = totalOutputTokens
			return &result, nil
		}

		if len(toolUses) == 0 {
			// ToolChoice: OfAny forces at least one tool call, so this is
			// defensive against an API/behavior change rather than an
			// expected path — no read tool to execute and nothing proposed
			// means the loop genuinely has nothing to do next.
			return nil, fmt.Errorf("model turn produced no tool call and no proposal")
		}

		// Replay the model's own turn (its narration text plus every
		// tool_use block, verbatim) before the tool_result turn that
		// answers it — required so the next call has the tool_use ids the
		// results below reference, and (with adaptive thinking on) so any
		// thinking block that preceded the tool calls is preserved.
		messages = append(messages, message.ToParam())

		resultBlocks := make([]anthropic.ContentBlockParamUnion, 0, len(toolUses))
		for _, tu := range toolUses {
			output, err := toolExec(ctx, tu.Name, tu.Input)
			isError := err != nil
			if err != nil {
				output = err.Error()
			}
			resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(tu.ID, output, isError))
		}
		messages = append(messages, anthropic.NewUserMessage(resultBlocks...))
	}

	return nil, fmt.Errorf("model did not call propose_changes within %d tool-loop iterations", maxToolIterations)
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

// currentText concatenates the accumulated text blocks of a (possibly
// partial) message — the model's narration, not the proposal itself, which
// arrives as a tool call's input rather than text (see Generate).
func currentText(message anthropic.Message) string {
	var text strings.Builder
	for _, block := range message.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String()
}
