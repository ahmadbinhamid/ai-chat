// Package ai turns chat prompts into theme file changes via a read/explore/propose_changes tool loop.
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
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// themeEngineSpec is THEME_ENGINE_SPEC.md embedded at build time.
//go:embed prompts/theme_engine_spec.md
var themeEngineSpec string

// Turn is one prior conversation turn, replayed as grounding.
type Turn struct {
	Role    string // "user" or "assistant"
	Content string
}

// Image is attached; old turn images never resurface (token cost bound). MediaType: image/jpeg, png, gif, webp.
type Image struct {
	Base64    string
	MediaType string
}

// GeneratedFile is a proposed file. Action "edit" is wire-format only; materializeEdits converts to "create"/"update".
type GeneratedFile struct {
	Path    string `json:"path"`
	Action  string `json:"action"` // "create" | "update" | "edit"
	Content string `json:"content"`
	Edits   []Edit `json:"edits"`
	// OriginalAction: saved for recapAssistantTurn replay to avoid training model toward whole-file rewrites.
	OriginalAction string `json:"-"`
}

// Edit is a find/replace pair; old_string must match current content exactly once, new_string may be empty.
type Edit struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// Result is the model's final answer, delivered as propose_changes tool input.
type Result struct {
	Summary            string `json:"summary"`
	NeedsClarification bool   `json:"needs_clarification"`
	// AnsweredQuestion: true when summary is a direct answer, not a change. Distinguishes from fabricated "done" over empty.
	AnsweredQuestion   bool               `json:"answered_question"`
	Files              []GeneratedFile    `json:"files"`
	PageRegistryEntry  *themefs.PageEntry `json:"page_registry_entry"`
	LayoutLinksToAdd   []string           `json:"layout_links_to_add"`
	LayoutScriptsToAdd []string           `json:"layout_scripts_to_add"`
	InputTokens        int64              `json:"-"`
	OutputTokens       int64              `json:"-"`
	// ExplorationToolCalls: distinguishes hallucinated empty from explored-and-empty via isUnexploredEmptyProposal.
	ExplorationToolCalls int `json:"-"`
}

// GenerationMode restricts what a turn is allowed to touch.
const (
	GenerationModeEdit  = "edit"   // default: any tool, any file; empty string means edit
	GenerationModeBrand = "brand"  // only defaults.json, only propose_changes
	GenerationModeCopy  = "copy"   // hardcoded component/page text only
	GenerationModePages = "pages"  // adding new pages.json-registered pages
)

// ThemeContext is current theme state for Claude; model fetches file content via read_theme_file.
type ThemeContext struct {
	ThemeSlug    string
	PagesJSON    string                 // current pages.json or ""
	DefaultsJSON string                 // current defaults.json
	FileTree     []themefs.FileTreeEntry // supplied up front to avoid initial list_theme_files cost
	Manifest     *themefs.Manifest       // component param signatures; nil if unavailable
	GenerationMode string               // restricts what this turn may touch; empty = GenerationModeEdit
}

// Generator calls Claude to produce theme file changes.
type Generator struct {
	client anthropic.Client
	model  anthropic.Model
	effort anthropic.OutputConfigEffort
	visionModel anthropic.Model  // separate model for image calls; text-only turns use model for proven quality
	fake      bool
	fakeDelay time.Duration
	maxTokens int64               // Claude call's max_tokens; see AI_MAX_TOKENS env var
	streamTimeouts StreamTimeouts // zero-valued fields fall back to defaults, never to instant timeout
}

// StreamTimeouts configures idle and first-token budgets for consumeStream.
// Zero-valued fields fall back to defaults via (*Generator).idleTimeout/firstTokenTimeoutFor.
// FirstToken* is split by GenerationMode: Brand/Copy are shorter (narrow scope), Pages is longer (most work).
type StreamTimeouts struct {
	Idle            time.Duration
	FirstTokenEdit  time.Duration
	FirstTokenBrand time.Duration
	FirstTokenCopy  time.Duration
	FirstTokenPages time.Duration
}

// Default stream timeouts, used when StreamTimeouts fields are zero.
const (
	defaultStreamIdleTimeout       = 12 * time.Second
	defaultFirstTokenTimeoutEdit   = 120 * time.Second
	defaultFirstTokenTimeoutNarrow = 45 * time.Second
	defaultFirstTokenTimeoutPages  = 150 * time.Second
)

func (g *Generator) idleTimeout() time.Duration {
	if g.streamTimeouts.Idle > 0 {
		return g.streamTimeouts.Idle
	}
	return defaultStreamIdleTimeout
}

// firstTokenTimeoutFor picks the first-token budget for mode (ai.GenerationMode value).
func (g *Generator) firstTokenTimeoutFor(mode string) time.Duration {
	switch mode {
	case GenerationModeBrand:
		if g.streamTimeouts.FirstTokenBrand > 0 {
			return g.streamTimeouts.FirstTokenBrand
		}
		return defaultFirstTokenTimeoutNarrow
	case GenerationModeCopy:
		if g.streamTimeouts.FirstTokenCopy > 0 {
			return g.streamTimeouts.FirstTokenCopy
		}
		return defaultFirstTokenTimeoutNarrow
	case GenerationModePages:
		if g.streamTimeouts.FirstTokenPages > 0 {
			return g.streamTimeouts.FirstTokenPages
		}
		return defaultFirstTokenTimeoutPages
	default: // GenerationModeEdit, or unset/empty — the common case.
		if g.streamTimeouts.FirstTokenEdit > 0 {
			return g.streamTimeouts.FirstTokenEdit
		}
		return defaultFirstTokenTimeoutEdit
	}
}

// clampToContextDeadline shortens d to ctx's deadline if sooner; first-token budget cannot outlive its generation.
func clampToContextDeadline(ctx context.Context, d time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return d
	}
	if remaining := time.Until(deadline); remaining < d {
		if remaining < 0 {
			return 0
		}
		return remaining
	}
	return d
}

// New constructs the client. apiKey empty is a startup error.
// DeepSeek's Anthropic-compat endpoint: tool_choice forcing NOT reliably honored; see zero-tool-calls handling in Generate.
func New(apiKey, baseURL, model, effort, visionModel string, maxTokens int64, streamTimeouts StreamTimeouts) (*Generator, error) {
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
	slog.Info("ai: generator configured",
		"model", model,
		"effort", effort,
		"max_tokens", maxTokens,
		"base_url_set", baseURL != "",
		"adaptive_thinking_supported", adaptiveThinkingSupported,
		"vision_model", visionModel)
	return &Generator{
		client:         anthropic.NewClient(opts...),
		model:          model,
		effort:         anthropic.OutputConfigEffort(effort),
		streamTimeouts: streamTimeouts,
		maxTokens:      maxTokens,
		visionModel:    visionModel,
	}, nil
}

// SupportsVision reports whether this Generator was configured with a
// vision-capable model — Generate rejects an Image when this is false
// rather than silently sending it to a model that can't use it.
func (g *Generator) SupportsVision() bool {
	return g.visionModel != ""
}

// NewFake builds a Generator that never calls Claude; used to test plumbing without spending tokens.
func NewFake(fakeDelay time.Duration) *Generator {
	return &Generator{fake: true, fakeDelay: fakeDelay}
}

// fakeGenerate: NewFake implementation. No changes proposed to avoid corrupting real themes.
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

// newTestGenerator builds a test Generator against a caller-supplied base URL.
func newTestGenerator(client anthropic.Client) *Generator {
	return &Generator{client: client, model: "test-model", effort: anthropic.OutputConfigEffortMedium, maxTokens: defaultMaxTokens}
}

// resultSchema is propose_changes' input_schema; additionalProperties: false matches API requirements.
var resultSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"summary", "needs_clarification", "answered_question", "files", "page_registry_entry", "layout_links_to_add", "layout_scripts_to_add"},
	"properties": map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"description": "1-3 plain-language sentences for the merchant-facing chat UI. If needs_clarification is true, this is the clarifying question instead. If answered_question is true, this is the direct answer instead.",
		},
		"needs_clarification": map[string]any{
			"type":        "boolean",
			"description": "true if the request conflicts with a hard rule in the spec or is too ambiguous to safely generate. When true, files must be empty.",
		},
		"answered_question": map[string]any{
			"type": "boolean",
			"description": "true when the merchant asked a question or made a read-only request (e.g. \"what does this say\", " +
				"\"can you read this and describe it\") and summary is your direct answer — not an attempted or completed " +
				"change. When true, files/page_registry_entry/layout_links_to_add/layout_scripts_to_add must all be empty, " +
				"the same as needs_clarification. Mutually exclusive with needs_clarification and with actually proposing " +
				"changes.",
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

// adaptiveThinkingSupported gates thinking: {type: "adaptive"} and output_config.effort.
const adaptiveThinkingSupported = true

// maxToolIterations bounds the read/explore loop; real page-creation prompts use full budget gathering context.
const maxToolIterations = 28

// forceProposeWithinLastN: distance from ceiling where model is forced to commit to a proposal.
const forceProposeWithinLastN = 3

// thrashOutputTokenThreshold: flags iterations with high output but exploration-only, no propose_changes (diagnostic only).
const thrashOutputTokenThreshold = 5000

// explorationToolNames are the read-only tools a tool-loop iteration can
// call besides propose_changes — see tools.go's toolName* constants.
var explorationToolNames = map[string]bool{
	toolNameListThemeFiles: true,
	toolNameReadThemeFile:  true,
	toolNameGrepTheme:      true,
}

// allExplorationTools reports whether all names are read-only exploration tools (never propose_changes).
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

// streamAccumulateMaxAttempts: retries on truncated/garbled chunks (transport hiccup); 3 * 5s = 10s max.
const streamAccumulateMaxAttempts = 3
const streamAccumulateRetryDelay = 5 * time.Second

// isRetryableAccumulateErr reports whether err is a truncated/garbled stream chunk.
func isRetryableAccumulateErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "accumulate stream")
}

// errStreamIdle and errStreamFirstToken: consumeStream timeout classes, both retried like truncated chunks.
var (
	errStreamIdle       = errors.New("provider stream idle timeout")
	errStreamFirstToken = errors.New("provider stream first-token timeout")
)

// isRetryableStreamErr reports if err is a provider hiccup (timeout or garbled), not a deadline/cancel.
func isRetryableStreamErr(err error) bool {
	return isRetryableAccumulateErr(err) || errors.Is(err, errStreamIdle) || errors.Is(err, errStreamFirstToken)
}

// streamRetryReason labels err for retry warning log (diagnostic only).
func streamRetryReason(err error) string {
	switch {
	case errors.Is(err, errStreamIdle):
		return "idle_timeout"
	case errors.Is(err, errStreamFirstToken):
		return "first_token_timeout"
	case isRetryableAccumulateErr(err):
		return "truncated_stream"
	default:
		return "unknown"
	}
}

// streamProgressBytes: text + thinking + tool_use count (tool_use alone can be real progress without narration).
func streamProgressBytes(message anthropic.Message) int {
	n := 0
	for _, block := range message.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			n += len(b.Text)
		case anthropic.ThinkingBlock:
			n += len(b.Thinking)
		case anthropic.ToolUseBlock:
			n += len(b.Name) + len(b.Input)
		}
	}
	return n
}

// consumeStream drains one streaming attempt into message. Enforces idle timeout (reset per event) and
// first-token timeout (until first progress per streamProgressBytes). stream.Next() has no timeout and
// blocks indefinitely on stalled connections, so each read runs in a goroutine with timers in a select.
func consumeStream(
	ctx context.Context,
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion],
	message *anthropic.Message,
	coalescer *deltaCoalescer,
	idleTimeout, firstTokenTimeout time.Duration,
) error {
	emitted := 0
	sawProgress := false

	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()
	firstTokenTimer := time.NewTimer(firstTokenTimeout)
	defer firstTokenTimer.Stop()

	// Run stream.Next() in goroutine; select on: read completion, ctx deadline, idle/first-token timers.
	type nextResult struct{ ok bool }
	nextCh := make(chan nextResult, 1)
	readNext := func() { nextCh <- nextResult{ok: stream.Next()} }
	go readNext()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idleTimer.C:
			return errStreamIdle
		case <-firstTokenTimer.C:
			if !sawProgress {
				return errStreamFirstToken
			}
			// Stale fire (Stop() returned false, timer not drained); sawProgress true = no-op.
		case r := <-nextCh:
			if !r.ok {
				return nil
			}
			if err := message.Accumulate(stream.Current()); err != nil {
				return fmt.Errorf("accumulate stream: %w", err)
			}
			if !sawProgress && streamProgressBytes(*message) > 0 {
				sawProgress = true
				firstTokenTimer.Stop()
			}
			if full := currentText(*message); len(full) > emitted {
				coalescer.add(full[emitted:])
				emitted = len(full)
			}
			// Drain idleTimer if Stop() returned false; reset and read next.
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)
			go readNext()
		}
	}
}

// defaultMaxTokens: used when AI_MAX_TOKENS is unset; above 32000 to avoid truncating large proposals.
const defaultMaxTokens = 64000

// errMaxTokensTruncated: returned when StopReason==max_tokens to prevent parsing partial JSON.
var errMaxTokensTruncated = errors.New("model response was truncated at the max_tokens limit before propose_changes could be parsed")

// Generate orchestrates tool loop: execute tools until propose_changes; materialize edits before returning.
// onDelta: called per text chunk streamed. progress: notified per toolExec call.
// Materialization failure fed back as tool_result; loop continues, giving model chance to correct.
func (g *Generator) Generate(ctx context.Context, tc ThemeContext, history []Turn, prompt string, images []Image, onDelta func(string), progress ToolProgress, toolExec ToolExecutor, readFile FileReader) (*Result, error) {
	if g.fake {
		return g.fakeGenerate(ctx, prompt)
	}
	// Defense in depth: public API, check vision model.
	if len(images) > 0 && !g.SupportsVision() {
		return nil, fmt.Errorf("image attached but no vision model is configured")
	}
	// Use vision model only when images attached; text-only turns use proven text model.
	callModel := g.model
	if len(images) > 0 {
		callModel = g.visionModel
	}
	// editFailureCounts: path tracking for materializeEdits fallback (full content vs retry forever).
	editFailureCounts := make(map[string]int)
	// knownPaths: every path with grounding (read_theme_file or "create" action). Backs warnReadBeforeWriteViolations.
	knownPaths := make(map[string]bool)
	// Anthropic rejects empty text blocks; skip empty turns (defense in depth).
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
	// Mark end of replayed history as cache breakpoint (byte-identical across turns).
	if last := len(messages) - 1; last >= 0 {
		lastBlock := messages[last].Content[len(messages[last].Content)-1]
		if lastBlock.OfText != nil && lastBlock.OfText.Text != "" {
			cacheControl := anthropic.NewCacheControlEphemeralParam()
			cacheControl.TTL = anthropic.CacheControlEphemeralTTLTTL1h
			lastBlock.OfText.CacheControl = cacheControl
		}
	}
	if len(images) > 0 {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(images)+1)
		for _, img := range images {
			blocks = append(blocks, anthropic.NewImageBlockBase64(img.MediaType, img.Base64))
		}
		blocks = append(blocks, anthropic.NewTextBlock(prompt))
		messages = append(messages, anthropic.NewUserMessage(blocks...))
	} else {
		messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))
	}

	tools := toolsForMode(tc.GenerationMode)
	// Clamp first-token timeout to ctx deadline (slow iterations tighten budget naturally).
	firstTokenTimeout := clampToContextDeadline(ctx, g.firstTokenTimeoutFor(tc.GenerationMode))
	// Dynamic block (pages.json, defaults.json, file tree, manifest): byte-identical across iterations/retries.
	// Cache breakpoint saves ~10% cost; minimum prefix length checked silently by API.
	dynamicBlock := anthropic.TextBlockParam{Text: dynamicSystemPrompt(tc)}
	dynamicCacheControl := anthropic.NewCacheControlEphemeralParam()
	dynamicCacheControl.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	dynamicBlock.CacheControl = dynamicCacheControl
	system := []anthropic.TextBlockParam{staticSystemPromptBlock(), dynamicBlock}

	var totalInputTokens, totalOutputTokens int64
	// explorationToolCalls: counts list_theme_files/read_theme_file/grep_theme (never propose_changes).
	explorationToolCalls := 0
	// Diagnostics: generateStart, modelElapsed, toolElapsed, iterationsUsed (summary log on return).
	generateStart := time.Now()
	var totalModelElapsed, totalToolElapsed time.Duration
	iterationsUsed := 0
	// totalReasoningTokens/reasoningTokensReported: theory 1 (reasoning tax) diagnostics.
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
			// Near ceiling: force propose_changes to commit from gathered context, not run out of budget reading.
			toolChoice = anthropic.ToolChoiceParamOfTool(toolNameProposeChanges)
			slog.Info("ai: forcing propose_changes near tool-loop budget ceiling",
				"iteration", iteration, "max_tool_iterations", maxToolIterations)
		}
		params := anthropic.MessageNewParams{
			Model:      callModel,
			MaxTokens:  g.maxTokens,
			System:     system,
			Messages:   messages,
			Tools:      tools,
			ToolChoice: toolChoice,
		}
		if forcingPropose {
			// Nudge: don't invent placeholder paths; use needs_clarification + empty files if not ready.
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
		if adaptiveThinkingSupported {
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
			params.OutputConfig = anthropic.OutputConfigParam{Effort: g.effort}
		}
		var message anthropic.Message
		// Track attempts separately to distinguish retried streams from slow inference.
		modelCallStart := time.Now()
		attemptsUsed := 0
		for attempt := 1; attempt <= streamAccumulateMaxAttempts; attempt++ {
			attemptsUsed = attempt
			stream := g.client.Messages.NewStreaming(ctx, params)
			message = anthropic.Message{}
			// Fresh per attempt; coalescer.flush() empties before next attempt. Retries re-emit onDelta from start (acceptable).
			coalescer := newDeltaCoalescer(onDelta)
			streamErr := consumeStream(ctx, stream, &message, coalescer, g.idleTimeout(), firstTokenTimeout)
			// Flush remaining buffered text; close immediately (timeouts may abandon mid-read).
			coalescer.flush()
			_ = stream.Close()
			if streamErr == nil {
				if err := stream.Err(); err != nil {
					// "provider" not "claude": serves DeepSeek too (Anthropic-compat endpoint).
					return nil, fmt.Errorf("provider stream: %w", err)
				}
				break
			}
			if !isRetryableStreamErr(streamErr) || attempt == streamAccumulateMaxAttempts {
				return nil, streamErr
			}
			// Transport/provider hiccup; pause and retry.
			slog.Warn("ai: provider stream disrupted, retrying", "attempt", attempt,
				"max_attempts", streamAccumulateMaxAttempts, "reason", streamRetryReason(streamErr),
				"error", streamErr.Error())
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
		// ThinkingTokens may be unreported (reasoningTokensValid false) vs. genuinely zero.
		reasoningTokens := message.Usage.OutputTokensDetails.ThinkingTokens
		reasoningTokensValid := message.Usage.OutputTokensDetails.JSON.ThinkingTokens.Valid()
		totalReasoningTokens += reasoningTokens
		if reasoningTokensValid {
			reasoningTokensReported = true
		}

		var toolUses []anthropic.ContentBlockUnion
		var proposeInput json.RawMessage
		// Count text blocks/chars off accumulated message (not SSE deltas); no double-counting on retried attempts.
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
		// Model latency/tokens; cache_read_input_tokens > 0 on iteration 2+ confirms caching works.
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

		// Flag thrash pattern (exploration-only, high output): diagnostic only, no behavior change.
		if allExplorationTools(toolNames) && message.Usage.OutputTokens > thrashOutputTokenThreshold {
			slog.Warn("ai: tool-loop iteration spent unusually many output tokens on exploration only",
				"iteration", iteration, "output_tokens", message.Usage.OutputTokens, "tools_called", toolNames)
		}

		// StopReason == max_tokens: proposal truncated mid-stream; fail explicitly, don't parse partial JSON.
		if message.StopReason == anthropic.StopReasonMaxTokens {
			return nil, errMaxTokensTruncated
		}

		// materializeFailureMsg: fed back as tool_result (isError: true) on failure; loop continues for correction.
		var materializeFailureMsg string
		if proposeInput != nil {
			var result Result
			if err := json.Unmarshal(proposeInput, &result); err != nil {
				return nil, fmt.Errorf("could not parse propose_changes input: %w", err)
			}
			// Register creates even if materialization fails (grounding for later edits).
			for _, f := range result.Files {
				if f.Action == "create" {
					knownPaths[f.Path] = true
				}
			}
			ok, retryMsg := materializeEdits(ctx, &result, readFile, editFailureCounts)
			if ok {
				warnReadBeforeWriteViolations(result.Files, knownPaths)
				result.InputTokens = totalInputTokens
				result.OutputTokens = totalOutputTokens
				result.ExplorationToolCalls = explorationToolCalls
				return &result, nil
			}
			slog.Warn("ai: propose_changes edit materialization failed, retrying", "iteration", iteration)
			materializeFailureMsg = retryMsg
		}

		if len(toolUses) == 0 {
			slog.Warn("ai: tool-loop nudge fired (zero tool calls despite forced tool_choice)", "iteration", iteration)
			// DeepSeek does NOT honor ToolChoice: OfAny (Anthropic does). Nudge instead of failing.
			messages = append(messages, message.ToParam())
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(
				"You must call one of the available tools on every turn — propose_changes if you already have enough "+
					"to finish (even for a simple greeting or question, propose_changes with no file changes, "+
					"answered_question: true, and the reply in `summary` is correct), or a read/explore tool otherwise. "+
					"A plain text reply with no tool call is not valid here.",
			)))
			continue
		}

		// Replay model's turn (narration + tool_use blocks) before tool_results; required for tool IDs and thinking.
		messages = append(messages, message.ToParam())

		resultBlocks := make([]anthropic.ContentBlockParamUnion, 0, len(toolUses))
		for _, tu := range toolUses {
			// propose_changes already handled above; here it receives failure description if materialization failed.
			if tu.Name == toolNameProposeChanges {
				resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(tu.ID, materializeFailureMsg, true))
				continue
			}
			if tu.Name == toolNameReadThemeFile {
				registerReadPaths(tu.Input, knownPaths) // Track read requests for warnReadBeforeWriteViolations.
			}
			explorationToolCalls++
			if progress != nil {
				progress.ToolStarted(tu.Name, tu.Input)
			}
			toolCallStart := time.Now()
			output, err := toolExec(ctx, tu.Name, tu.Input)
			toolElapsed := time.Since(toolCallStart)
			totalToolElapsed += toolElapsed
			// Distinguish tool-execution latency (HTTP to FlowPOS) from model latency.
			slog.Info("ai: tool exec timing", "iteration", iteration, "tool", tu.Name, "elapsed_ms", toolElapsed.Milliseconds(), "error", err != nil)
			isError := err != nil
			if progress != nil {
				// Summarize before output is overwritten with err.Error().
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

// readThemeFileToolInput: mirrors read_theme_file's "paths" field; independent from themebuild (package boundary).
type readThemeFileToolInput struct {
	Paths []string `json:"paths"`
}

// registerReadPaths: records read requests regardless of success/failure (per-path 404s opaque to ai package).
// Malformed input ignored; execReadThemeFile will error moments later.
func registerReadPaths(input json.RawMessage, knownPaths map[string]bool) {
	var args readThemeFileToolInput
	if err := json.Unmarshal(input, &args); err != nil {
		return
	}
	for _, p := range args.Paths {
		knownPaths[p] = true
	}
}

// warnReadBeforeWriteViolations: logs every action:"update" not in knownPaths (detection only, no behavior change).
// Scoped to THIS Generate call; files not read here have no grounding across turns.
// preSuppliedFiles: pages.json, defaults.json (spec §0 pre-supplied); edits to layout-start/layout-end still require reads.
var preSuppliedFiles = map[string]bool{
	"pages.json":    true,
	"defaults.json": true,
}

func warnReadBeforeWriteViolations(files []GeneratedFile, knownPaths map[string]bool) {
	for _, f := range files {
		if f.Action != "update" {
			continue
		}
		if preSuppliedFiles[f.Path] {
			continue
		}
		if knownPaths[f.Path] {
			continue
		}
		slog.Warn("ai: proposal writes to a file never read this generation",
			"path", f.Path, "action", f.Action, "file_count", len(files))
	}
}

// summarizeMaxTokens caps the summary completion — this is a cheap plain-
// text call (no tools, no thinking), so it needs nowhere near g.maxTokens;
// a few paragraphs of prose fits comfortably within this.
const summarizeMaxTokens = 1024

// Summarize: concise prose summary of turns as prior context (plain completion, no tools/thinking/system prompt).
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

// staticSystemPromptBlock: theme engine spec + fixed rules (byte-identical across calls); cache_control for 1h reuse.
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
   question to the merchant instead. If you set answered_question, summary is your direct answer
   instead — see the spec's §0 case split for when that applies (a question or read-only request,
   never an attempted or completed change) and rule 15.
8. Before you modify any existing file, read it with read_theme_file — never write a file you
   have not read, and never guess at its current content. Emit only files whose content actually
   changes as a result of this request.
9. Once you've read an existing file, action: "edit" is the default way to change it —
   old_string/new_string pairs materialize into the same complete corrected file, just cheaper to
   express. Use action: "update" only for a specific reason: the change is broad enough that a
   full rewrite is genuinely smaller than expressing it as edits.
10. Use list_theme_files/read_theme_file/grep_theme as needed to explore the theme before you
    finalize anything. Call propose_changes exactly once, when you're done, with the complete,
    final set of changes for this request — not a partial draft.`, themeEngineSpec),
		CacheControl: cacheControl,
	}
}

// dynamicSystemPrompt: per-request grounding (theme, routes, defaults, file tree, mode); no cache_control.
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

// formatManifest: renders component param index if supplied, "" otherwise (no placeholder).
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

// modeRestrictionNote: spells out MODE's restriction inline (model has no other way to learn it).
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

// formatFileTree: indented plain-text file tree (compact/readable, not JSON, for system prompt).
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

// coalesceInterval/coalesceMaxChars: bound onDelta fire rate to avoid per-token volume (keep live < 200ms).
const (
	coalesceInterval = 200 * time.Millisecond
	coalesceMaxChars = 80
)

// deltaCoalescer batches onDelta per-token chunks. NOT safe for concurrent use (single goroutine only).
type deltaCoalescer struct {
	onDelta   func(string)
	buf       strings.Builder
	lastFlush time.Time
}

func newDeltaCoalescer(onDelta func(string)) *deltaCoalescer {
	return &deltaCoalescer{onDelta: onDelta, lastFlush: time.Now()}
}

// add appends chunk; flushes if limit hit. No-op if onDelta nil (caller needs no nil check).
func (c *deltaCoalescer) add(chunk string) {
	if c.onDelta == nil {
		return
	}
	c.buf.WriteString(chunk)
	if c.buf.Len() >= coalesceMaxChars || time.Since(c.lastFlush) >= coalesceInterval {
		c.flush()
	}
}

// flush: sends buffered text (no-op if empty). Called from add() and by Generate after streaming ends.
func (c *deltaCoalescer) flush() {
	if c.buf.Len() == 0 {
		return
	}
	text := c.buf.String()
	c.buf.Reset()
	c.lastFlush = time.Now()
	c.onDelta(text)
}

// currentText concatenates text and thinking blocks (with adaptive thinking, narration in ThinkingBlock).
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

// maxToolResultSummaryChars: summary persisted in event payload; kept under 40-char budget.
const maxToolResultSummaryChars = 40

// summarizeToolResult: line/match/entry counts for display; approximations from output (not authoritative).
func summarizeToolResult(name, output string, toolErr error) string {
	if toolErr != nil {
		return truncateSummary("failed: "+toolErr.Error(), maxToolResultSummaryChars)
	}
	switch name {
	case toolNameListThemeFiles:
		// Count "path": occurrences in JSON output to approximate file+directory count.
		return fmt.Sprintf("%d entries", strings.Count(output, `"path":`))
	case toolNameReadThemeFile:
		return fmt.Sprintf("%d lines", strings.Count(output, "\n"))
	case toolNameGrepTheme:
		return summarizeGrepResult(output)
	default:
		return truncateSummary(output, maxToolResultSummaryChars)
	}
}

// summarizeGrepResult: counts match lines (excludes "(no matches)" and "(stopped at ...)" notes).
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

// truncateSummary: bounds to max runes, collapsing newlines (summary is single display line).
func truncateSummary(s string, max int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}
