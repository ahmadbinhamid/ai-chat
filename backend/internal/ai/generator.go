// Package ai wraps the Anthropic Claude API to turn a merchant's chat prompt
// into theme file changes (Liquid pages/components, CSS, JS), following the
// engine convention in prompts/theme_engine_spec.md. The response is
// constrained to a fixed JSON schema (Structured Outputs) so the caller gets
// typed, directly-writable file records — never markdown-fenced prose to
// scrape.
package ai

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
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

// Result is the model's structured reply.
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

// ThemeContext is the current theme state given to Claude as grounding —
// only what composing/editing a page actually needs (THEME_ENGINE_SPEC.md
// §5/§6), not a dump of every file in the theme.
type ThemeContext struct {
	ThemeSlug    string
	PagesJSON    string // current pages.json content, or "" if none yet
	DefaultsJSON string // current defaults.json content
	// EditingFiles holds the current content of specific files the request
	// targets for an edit (e.g. the page being modified) — empty for a
	// from-scratch "create a new page" request. Keyed by theme-relative path.
	EditingFiles map[string]string
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

// resultSchema is the JSON Schema Claude's response is constrained to
// (Structured Outputs) — see AI_THEME_BUILDER_PROMPT.md §3 for the full
// design rationale. additionalProperties: false on every object matches the
// SDK's structured-output requirements.
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

// Generate asks Claude for the file changes implementing prompt, given the
// theme context and prior conversation turns. onDelta, if non-nil, is called
// with each new chunk of raw model output as it streams in — the call
// itself always streams internally (regardless of onDelta) so a large page
// + CSS + JS response doesn't risk an HTTP timeout; wiring onDelta through
// to a live SSE response is a caller-side addition, not required for
// Generate to work.
func (g *Generator) Generate(ctx context.Context, tc ThemeContext, history []Turn, prompt string, onDelta func(string)) (*Result, error) {
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
	// EditingFiles rides along with the new prompt, not the system block —
	// see editingFilesBlock's doc comment for why that matters for caching.
	promptWithContext := prompt
	if len(tc.EditingFiles) > 0 {
		promptWithContext = fmt.Sprintf(
			"## Files this request can reference (real current content of every file this chat has touched before)\n%s\n\n%s",
			editingFilesBlock(tc.EditingFiles), prompt,
		)
	}
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(promptWithContext)))

	stream := g.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     g.model,
		MaxTokens: 32000,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: g.effort,
			Format: anthropic.JSONOutputFormatParam{Schema: resultSchema},
		},
		System: []anthropic.TextBlockParam{
			staticSystemPromptBlock(),
			{Text: dynamicSystemPrompt(tc)},
		},
		Messages: messages,
	})

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

	result, err := parseResult(currentText(message))
	if err != nil {
		return nil, err
	}
	result.InputTokens = message.Usage.InputTokens
	result.OutputTokens = message.Usage.OutputTokens
	return result, nil
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
8. Before proposing a file, check "Files this request can reference" in the next message for its
   current real content — if a path you're about to touch already appears there, edit that
   content (preserve what the merchant didn't ask to change) instead of guessing or rewriting it
   from scratch.`, themeEngineSpec),
		CacheControl: cacheControl,
	}
}

// dynamicSystemPrompt is the per-request grounding that varies on every call
// — which theme, its current route registry, and its brand defaults — so it
// carries no cache_control (see staticSystemPromptBlock for the part that
// does). EditingFiles deliberately isn't here — see editingFilesBlock.
func dynamicSystemPrompt(tc ThemeContext) string {
	pagesJSON := tc.PagesJSON
	if pagesJSON == "" {
		pagesJSON = "[]"
	}
	defaultsJSON := tc.DefaultsJSON
	if defaultsJSON == "" {
		defaultsJSON = "{}"
	}

	return fmt.Sprintf(`## Theme being edited
- Theme slug: %s
- Current pages.json (existing routes — never register a slug that's already here):
%s
- Current defaults.json (brand colors, fonts, menu, footer — match this, don't invent a different palette):
%s
`, tc.ThemeSlug, pagesJSON, defaultsJSON)
}

// editingFilesBlock formats the "files this request can reference" section
// — appended to the new user turn (see Generate) rather than kept in the
// system prompt, where it used to live alongside dynamicSystemPrompt.
// That mattered for more than tidiness: the system blocks precede the
// messages array, so anything in them is part of the byte-for-byte prefix
// the history cache breakpoint (the last message in a replayed
// conversation) depends on matching. EditingFiles reflects whatever the
// *previous* turn just wrote, so it changes on nearly every call in the
// normal "merchant iterates on their theme" flow — keeping it in the system
// prompt meant that breakpoint's cached prefix was invalidated almost every
// time, even though pagesJSON/defaultsJSON (still in dynamicSystemPrompt)
// change far less often. Moving it here means a multi-turn conversation
// actually gets the incremental-caching benefit that breakpoint was for.
func editingFilesBlock(editingFiles map[string]string) string {
	if len(editingFiles) == 0 {
		return "(none yet — this chat hasn't touched any existing file)"
	}
	paths := make([]string, 0, len(editingFiles))
	for path := range editingFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, path := range paths {
		fmt.Fprintf(&b, "\n### %s\n%s\n", path, editingFiles[path])
	}
	return b.String()
}

// currentText concatenates the accumulated text blocks of a (possibly
// partial) message.
func currentText(message anthropic.Message) string {
	var text strings.Builder
	for _, block := range message.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String()
}

// parseResult decodes the model's JSON reply, tolerating stray whitespace or
// markdown fences even though Structured Outputs should rule those out —
// cheap defensive parsing, mirroring the same guard used elsewhere against
// this exact model family.
func parseResult(raw string) (*Result, error) {
	s := strings.TrimSpace(raw)
	if start := strings.IndexByte(s, '{'); start >= 0 {
		if end := strings.LastIndexByte(s, '}'); end > start {
			s = s[start : end+1]
		}
	}
	var r Result
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return nil, fmt.Errorf("could not parse model response: %w", err)
	}
	return &r, nil
}
