// Package ai turns chat prompts into theme file changes via a read/explore/propose_changes tool loop.
package ai

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"time"

	"ai-chat/internal/aicatalog"
	"ai-chat/internal/themefs"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// themeEngineSpec is THEME_ENGINE_SPEC.md embedded at build time.
//
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

// AttachmentPlacement puts "Attached image N" (1-based, numbered across the whole chat) at Path under images/.
type AttachmentPlacement struct {
	Attachment int    `json:"attachment"`
	Path       string `json:"path"`
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
	// UseAttachments places merchant-attached images; the platform copies the real bytes, the model never writes them.
	UseAttachments []AttachmentPlacement `json:"use_attachments"`
	InputTokens    int64                 `json:"-"`
	OutputTokens   int64                 `json:"-"`
	// ExplorationToolCalls: distinguishes hallucinated empty from explored-and-empty via isUnexploredEmptyProposal.
	ExplorationToolCalls int `json:"-"`
	// CostUSD is what the provider charged across this result's calls; nil when it doesn't report cost.
	CostUSD *float64 `json:"-"`
	// ModelID/Effort: the catalogue model that produced this result, after any switch to the vision model.
	ModelID      string `json:"-"`
	Effort       string `json:"-"`
	conversation *Conversation
}

// Conversation returns this result's resumable conversation, or nil (fake mode, or not produced by Generate).
func (r *Result) Conversation() *Conversation {
	if r == nil {
		return nil
	}
	return r.conversation
}

// GenerationMode restricts what a turn is allowed to touch.
const (
	GenerationModeEdit  = "edit"  // default: any tool, any file; empty string means edit
	GenerationModeBrand = "brand" // only defaults.json, only propose_changes
	GenerationModeCopy  = "copy"  // hardcoded component/page text only
	GenerationModePages = "pages" // adding new pages.json-registered pages
)

// ThemeContext is current theme state for Claude; model fetches file content via read_theme_file.
type ThemeContext struct {
	ThemeSlug      string
	PagesJSON      string                  // current pages.json or ""
	DefaultsJSON   string                  // current defaults.json
	FileTree       []themefs.FileTreeEntry // supplied up front to avoid initial list_theme_files cost
	Manifest       *themefs.Manifest       // component param signatures; nil if unavailable
	GenerationMode string                  // restricts what this turn may touch; empty = GenerationModeEdit
	Model          aicatalog.Choice        // this turn's model and effort; zero = the catalogue default
	DraftPaths     []string                // files with unsaved changes from earlier turns; paths only, contents come via read_theme_file
	// StagedImagePaths are DraftPaths that are placed images: not in the live theme until the merchant applies.
	StagedImagePaths map[string]bool
	// Continue resumes a prior call's conversation (history and images are then ignored); nil = fresh call.
	Continue *Conversation
}

// Generator calls Claude to produce theme file changes.
type Generator struct {
	catalog *aicatalog.Catalog
	// One client per catalogue provider, built at startup and shared by every call to that provider.
	clients        map[string]anthropic.Client
	fake           bool
	fakeDelay      time.Duration
	maxTokens      int64          // Claude call's max_tokens; see AI_MAX_TOKENS env var
	streamTimeouts StreamTimeouts // zero-valued fields fall back to defaults, never to instant timeout
}

// StreamTimeouts configures idle and first-token budgets for consumeStream.
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
func New(catalog *aicatalog.Catalog, lookupEnv func(string) (string, bool), maxTokens int64, streamTimeouts StreamTimeouts) (*Generator, error) {
	if catalog == nil {
		return nil, fmt.Errorf("model catalogue is not set")
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	clients := make(map[string]anthropic.Client, len(catalog.Providers))
	for name, p := range catalog.Providers {
		key, ok := lookupEnv(p.APIKeyEnv)
		if !ok || key == "" {
			return nil, fmt.Errorf("provider %q: %s is not set", name, p.APIKeyEnv)
		}
		opts := []option.RequestOption{option.WithAPIKey(key)}
		if p.BaseURL != "" {
			opts = append(opts, option.WithBaseURL(p.BaseURL))
		}
		clients[name] = anthropic.NewClient(opts...)
	}
	def := catalog.Default()
	slog.Info("ai: generator configured",
		"default_model", catalog.DefaultModel,
		"default_turn_model", def.ModelID,
		"default_effort", def.Effort,
		"models", len(catalog.Models),
		"providers", len(clients),
		"vision_model", catalog.VisionModel,
		"summary_model", catalog.SummaryModel,
		"max_tokens", maxTokens)
	return &Generator{catalog: catalog, clients: clients, streamTimeouts: streamTimeouts, maxTokens: maxTokens}, nil
}

// SupportsVision reports whether the catalogue has a model that can see images; Generate rejects an Image otherwise.
func (g *Generator) SupportsVision() bool {
	return g.catalog.SupportsImages()
}

// Catalog is the model catalogue this Generator serves.
func (g *Generator) Catalog() *aicatalog.Catalog { return g.catalog }

// model resolves a choice to its catalogue entry, its provider's client and the catalogue's extra request fields.
func (g *Generator) model(ch aicatalog.Choice) (aicatalog.Model, anthropic.Client, []option.RequestOption, error) {
	m, ok := g.catalog.Model(ch.ModelID)
	if !ok {
		return aicatalog.Model{}, anthropic.Client{}, nil, fmt.Errorf("model %q is not in the catalogue", ch.ModelID)
	}
	client, ok := g.clients[m.Provider]
	if !ok {
		return aicatalog.Model{}, anthropic.Client{}, nil, fmt.Errorf("no client for provider %q", m.Provider)
	}
	return m, client, requestFieldOptions(g.catalog.RequestFields(m.ID)), nil
}

// requestFieldOptions sets each catalogue option as a top-level body field; sorted so requests are byte-identical,
// which provider-side prompt caching depends on.
func requestFieldOptions(fields map[string]any) []option.RequestOption {
	keys := slices.Sorted(maps.Keys(fields))
	opts := make([]option.RequestOption, 0, len(keys))
	for _, k := range keys {
		opts = append(opts, option.WithJSONSet(k, fields[k]))
	}
	return opts
}

// NewFake builds a Generator that never calls Claude; used to test plumbing without spending tokens.
func NewFake(fakeDelay time.Duration) *Generator {
	// Placeholder catalogue: fake mode never calls a provider, and without a vision model images stay rejected as before.
	cat, err := aicatalog.FromEnv("fake-mode", "", "fake-model", string(anthropic.OutputConfigEffortLow), "")
	if err != nil {
		panic(err)
	}
	return &Generator{fake: true, fakeDelay: fakeDelay, catalog: cat}
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
	g := &Generator{clients: map[string]anthropic.Client{"default": client}, maxTokens: defaultMaxTokens}
	g.setTestVisionModel("")
	return g
}

// setTestVisionModel rebuilds a test Generator's one-model catalogue with visionModel for image turns.
func (g *Generator) setTestVisionModel(visionModel string) {
	cat, err := aicatalog.FromEnv("test-key", "", "test-model", string(anthropic.OutputConfigEffortMedium), visionModel)
	if err != nil {
		panic(err)
	}
	g.catalog = cat
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
		// Strict + additionalProperties: false requires all properties present; documented in field descriptions, enforced server-side (DeepSeek endpoint has unverified conditional-subschema support).
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
		// No requires_auth: spec allows only on fixed system routes (my_account, etc); ai-chat creates only "custom" pages. Field omitted to avoid confusing model.
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
		// Optional, unlike the rest: most turns place nothing, and a text-recovered call that omits it must still match.
		// The platform copies the attachment's bytes on Apply; the model only names the image and where it goes.
		"use_attachments": map[string]any{
			"type": "array",
			"description": "Merchant-attached images to place into the theme — only when the merchant asks to use, " +
				"place, add or put an attached image, never for one sent as a look/style reference. The platform " +
				"copies the real image file; you never write image bytes. Reference the placed image from your " +
				"files with {{ 'images/<name>' | asset_url }}. Omit or [] when placing nothing.",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"attachment", "path"},
				"properties": map[string]any{
					"attachment": map[string]any{
						"type":        "integer",
						"description": "The N from \"Attached image N\" in the attached images list.",
					},
					"path": map[string]any{
						"type": "string",
						"description": "New file under images/, e.g. 'images/hero-coffee.jpg'. Its extension must " +
							"match the image's real type (.png, .jpg or .webp — never .jpeg or .svg), and it must " +
							"not already exist in the theme.",
					},
				},
			},
		},
	},
}

// maxToolIterations bounds the read/explore loop; real page-creation prompts use full budget gathering context.
const maxToolIterations = 28

// forceProposeAfterRounds: real redesigns propose within 2-8 rounds, so 12 leaves room; a turn past it is searching
// for a cause it hasn't found, and later rounds of that only get slower (30-45s each seen on DeepSeek).
const forceProposeAfterRounds = 12

// forceProposeAfter caps search time on one turn: a merchant waiting longer than this for "I couldn't find it" is worse
// off than getting the question sooner, whatever the round count.
const forceProposeAfter = 3 * time.Minute

// forceProposeWithinLastN: how many rounds below maxToolIterations are forced when the time limit doesn't fire first.
const forceProposeWithinLastN = maxToolIterations - forceProposeAfterRounds

// forceProposeCauseRule: a forced round once resubmitted a file with only its line endings changed and called it the fix.
const forceProposeCauseRule = "Only propose a change you can tie to a specific cause you found in the code. If you " +
	"can't name the cause, change nothing: set `needs_clarification: true` and ask the merchant one specific question."

// forceProposeInstruction: a forced turn that found nothing must ask, never claim a fix it didn't make.
const forceProposeInstruction = "Stop searching and call propose_changes now. " + forceProposeCauseRule + " " +
	"If you did find the cause, include the change. Never say something was fixed when no file changed, and never " +
	"resubmit a file with only whitespace or line-ending changes as a fix. Only include a file in `files` if you " +
	"actually read/verified its current content (for an update) or have real, complete content ready (for a create) " +
	"— never invent a placeholder path or partial content to fill the array."

// shouldForcePropose reports whether this round must propose: past forceProposeAfterRounds or forceProposeAfter.
func shouldForcePropose(iteration int, elapsed time.Duration) bool {
	return iteration >= forceProposeAfterRounds || elapsed >= forceProposeAfter
}

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

// errStreamReaderPanicked: not retryable — a stream that panicked once is not trusted to read again.
var errStreamReaderPanicked = errors.New("provider stream reader panicked")

// errStreamIdle and errStreamFirstToken: consumeStream timeout classes, both retried like truncated chunks.
var (
	errStreamIdle       = errors.New("provider stream idle timeout")
	errStreamFirstToken = errors.New("provider stream first-token timeout")
)

// isRetryableStreamErr reports if err is a provider hiccup (timeout or garbled), not a deadline/cancel.
func isRetryableStreamErr(err error) bool {
	return isRetryableAccumulateErr(err) || errors.Is(err, errStreamIdle) || errors.Is(err, errStreamFirstToken) ||
		isRetryableProviderErr(err)
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
	case classifyProviderError(err) == providerErrRateLimited:
		return "rate_limited"
	case classifyProviderError(err) == providerErrUpstream:
		return "upstream_failed"
	default:
		return "unknown"
	}
}

// streamProgressBytes: text + thinking + tool_use count (tool_use alone can be real progress without narration).
// Reads union fields, not AsAny(): AsAny re-decodes the content_block_start JSON, so deltas stay invisible until block stop.
func streamProgressBytes(message anthropic.Message) int {
	n := 0
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			n += len(block.Text)
		case "thinking":
			n += len(block.Thinking)
		case "tool_use":
			n += len(block.Name) + len(block.Input)
		}
	}
	return n
}

// consumeStream drains one streaming attempt into message. Enforces idle timeout (reset per event) and
func consumeStream(
	ctx context.Context,
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion],
	message *anthropic.Message,
	idleTimeout, firstTokenTimeout time.Duration,
) error {
	return consumeStreamDeltas(ctx, stream, message, idleTimeout, firstTokenTimeout, nil)
}

// consumeStreamDeltas is consumeStream that also hands each message_delta to onDelta: the accumulated message keeps
// only the usage fields the SDK knows, not a provider's extras such as OpenRouter's cost.
func consumeStreamDeltas(
	ctx context.Context,
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion],
	message *anthropic.Message,
	idleTimeout, firstTokenTimeout time.Duration,
	onDelta func(anthropic.MessageDeltaEvent),
) error {
	sawProgress := false

	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()
	firstTokenTimer := time.NewTimer(firstTokenTimeout)
	defer firstTokenTimer.Stop()

	// Run stream.Next() in goroutine; select on: read completion, ctx deadline, idle/first-token timers.
	type nextResult struct {
		ok  bool
		err error
	}
	nextCh := make(chan nextResult, 1)
	readNext := func() {
		// A panic here would crash the server; recovered, it must still reach the select below or the call hangs.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered panic reading the provider stream", "panic", r, "stack", string(debug.Stack()))
				nextCh <- nextResult{err: fmt.Errorf("%w: %v", errStreamReaderPanicked, r)}
			}
		}()
		nextCh <- nextResult{ok: stream.Next()}
	}
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
			if r.err != nil {
				return r.err
			}
			if !r.ok {
				return nil
			}
			event := stream.Current()
			if err := message.Accumulate(event); err != nil {
				return fmt.Errorf("accumulate stream: %w", err)
			}
			if onDelta != nil && event.Type == "message_delta" {
				onDelta(event.AsMessageDelta())
			}
			if !sawProgress && streamProgressBytes(*message) > 0 {
				sawProgress = true
				firstTokenTimer.Stop()
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

// maxConsecutiveTextOnlyRounds: one or two text-only replies are normal on DeepSeek; three in a row means it's stuck,
// and nudging on to maxToolIterations only burns minutes before failing anyway.
const maxConsecutiveTextOnlyRounds = 3

// errStuckInTextReplies is deliberately not a "too complex" failure: it fires on two-word messages too.
var errStuckInTextReplies = errors.New("model kept replying in plain text without calling a tool")

// errMaxTokensTruncated: returned when StopReason==max_tokens to prevent parsing partial JSON.
var errMaxTokensTruncated = errors.New("model response was truncated at the max_tokens limit before propose_changes could be parsed")

// Generate orchestrates tool loop: execute tools until propose_changes; materialize edits before re...
// Model text/thinking is never streamed to the merchant (it leaked reasoning and text-written tool calls); ToolProgress is the only feed.
func (g *Generator) Generate(ctx context.Context, tc ThemeContext, history []Turn, prompt string, images []Image, progress ToolProgress, toolExec ToolExecutor, readFile FileReader) (*Result, error) {
	if g.fake {
		return g.fakeGenerate(ctx, prompt)
	}
	// Defense in depth: public API, check vision model.
	if len(images) > 0 && !g.SupportsVision() {
		return nil, fmt.Errorf("image attached but no vision model is configured")
	}
	choice := tc.Model
	if choice.ModelID == "" {
		choice = g.catalog.Default()
	}
	if len(images) > 0 {
		visionChoice, switched, _ := g.catalog.ForImages(choice)
		if switched {
			slog.Info("ai: chosen model can't see images; the vision model handles this turn",
				"chosen_model", choice.ModelID, "vision_model", visionChoice.ModelID, "effort", visionChoice.Effort)
		}
		choice = visionChoice
	}
	// editFailureCounts: path tracking for materializeEdits fallback (full content vs retry forever).
	editFailureCounts := make(map[string]int)
	// knownPaths: every path with grounding (read_theme_file or "create" action). Backs warnReadBeforeWriteViolations.
	knownPaths := make(map[string]bool)
	var messages []anthropic.MessageParam
	if tc.Continue != nil {
		// Images already sit in the resumed history's first prompt; re-attaching would duplicate them.
		choice = tc.Continue.choice
		messages = tc.Continue.resumeMessages(prompt)
	} else {
		messages = freshMessages(history, prompt, images)
	}

	entry, client, reqOpts, err := g.model(choice)
	if err != nil {
		return nil, err
	}
	slog.Info("ai: turn model", "model_id", choice.ModelID, "model", entry.Model, "effort", choice.Effort,
		"thinking", entry.Thinking, "resumed", tc.Continue != nil)

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

	var totalInputTokens, totalOutputTokens, totalCacheReadTokens int64
	var cost costTotal
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
			"total_cache_read_tokens", totalCacheReadTokens,
			"total_cost_usd", cost.log(),
			"total_reasoning_tokens", totalReasoningTokens,
			"reasoning_tokens_reported", reasoningTokensReported)
	}()
	// Counts consecutive rounds with no tool call that text recovery also couldn't rescue; any real call resets it.
	consecutiveTextOnly := 0
	for iteration := 0; iteration < maxToolIterations; iteration++ {
		iterationsUsed = iteration + 1
		toolChoice := anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
		forcingPropose := shouldForcePropose(iteration, time.Since(generateStart))
		// The request always lists every tool, keeping the prompt cache intact; a forced round instead refuses any call
		// but propose_changes, since DeepSeek ignores a named tool_choice and keeps searching.
		allowedTools := tools
		if forcingPropose {
			toolChoice = anthropic.ToolChoiceParamOfTool(toolNameProposeChanges)
			allowedTools = []anthropic.ToolUnionParam{proposeChangesTool()}
			slog.Info("ai: forcing propose_changes",
				"iteration", iteration, "max_tool_iterations", maxToolIterations,
				"elapsed_ms", time.Since(generateStart).Milliseconds())
		}
		params := anthropic.MessageNewParams{
			Model:      entry.Model,
			MaxTokens:  g.maxTokens,
			System:     system,
			Messages:   messages,
			Tools:      tools,
			ToolChoice: toolChoice,
		}
		if forcingPropose {
			params.System = append(append([]anthropic.TextBlockParam{}, system...), anthropic.TextBlockParam{
				Text: forceProposeInstruction,
			})
		}
		// A model without thinking gets neither parameter.
		switch {
		case !entry.Thinking:
		case forcingPropose:
			// DeepSeek rejects a named tool_choice while thinking ("Thinking mode does not support this tool_choice"),
			// and thinks by default, so the forced call must disable it explicitly.
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
		default:
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
			if choice.Effort != "" {
				params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(choice.Effort)}
			}
		}
		var message anthropic.Message
		// Track attempts separately to distinguish retried streams from slow inference.
		modelCallStart := time.Now()
		attemptsUsed := 0
		for attempt := 1; attempt <= streamAccumulateMaxAttempts; attempt++ {
			attemptsUsed = attempt
			stream := client.Messages.NewStreaming(ctx, params, reqOpts...)
			message = anthropic.Message{}
			callCost, callCostReported := 0.0, false
			streamErr := consumeStreamDeltas(ctx, stream, &message, g.idleTimeout(), firstTokenTimeout, func(d anthropic.MessageDeltaEvent) {
				callCost, callCostReported = deltaCost(d)
			})
			// Close immediately (timeouts may abandon mid-read).
			_ = stream.Close()
			if streamErr == nil {
				err := stream.Err()
				if err == nil {
					cost.add(callCost, callCostReported)
					break
				}
				// "provider" not "claude": serves DeepSeek too (Anthropic-compat endpoint).
				streamErr = fmt.Errorf("provider stream: %w", err)
				if !isRetryableProviderErr(err) {
					alertProviderError(err, entry.Model)
					return nil, streamErr
				}
			}
			if !isRetryableStreamErr(streamErr) || attempt == streamAccumulateMaxAttempts {
				alertProviderError(streamErr, entry.Model)
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
		totalCacheReadTokens += message.Usage.CacheReadInputTokens
		// ThinkingTokens may be unreported (reasoningTokensValid false) vs. genuinely zero.
		reasoningTokens := message.Usage.OutputTokensDetails.ThinkingTokens
		reasoningTokensValid := message.Usage.OutputTokensDetails.JSON.ThinkingTokens.Valid()
		totalReasoningTokens += reasoningTokens
		if reasoningTokensValid {
			reasoningTokensReported = true
		}

		var toolUses []anthropic.ContentBlockUnion
		var proposeInput json.RawMessage
		var proposeID string
		// Count text blocks/chars off accumulated message (not SSE deltas); no double-counting on retried attempts.
		textBlockCount := 0
		textChars := 0
		thinkingBlockCount := 0
		thinkingChars := 0
		for _, block := range message.Content {
			if block.Type == "text" {
				textBlockCount++
				textChars += len(block.Text)
				continue
			}
			if block.Type == "thinking" {
				thinkingBlockCount++
				thinkingChars += len(block.Thinking)
				continue
			}
			if block.Type != "tool_use" {
				continue
			}
			toolUses = append(toolUses, block)
			if block.Name == toolNameProposeChanges {
				proposeInput = block.Input
				proposeID = block.ID
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
		// Lengths only, never content: this output is withheld from the merchant and may contain spec/theme internals.
		slog.Debug("ai: model output withheld from chat",
			"iteration", iteration,
			"text_block_count", textBlockCount,
			"text_chars", textChars,
			"thinking_block_count", thinkingBlockCount,
			"thinking_chars", thinkingChars)

		// Flag thrash pattern (exploration-only, high output): diagnostic only, no behavior change.
		if allExplorationTools(toolNames) && message.Usage.OutputTokens > thrashOutputTokenThreshold {
			slog.Warn("ai: tool-loop iteration spent unusually many output tokens on exploration only",
				"iteration", iteration, "output_tokens", message.Usage.OutputTokens, "tools_called", toolNames)
		}

		// StopReason == max_tokens: proposal truncated mid-stream; fail explicitly, don't parse partial JSON.
		if message.StopReason == anthropic.StopReasonMaxTokens {
			return nil, errMaxTokensTruncated
		}

		// DeepSeek sometimes writes propose_changes as text instead of a tool_use block; recover it rather than nudge.
		recovered := false
		if len(toolUses) == 0 {
			if args, ok := recoverProposeFromText(message); ok {
				proposeInput, recovered = args, true
				slog.Warn("ai: recovered a propose_changes call written as text", "iteration", iteration)
			}
		}

		if len(toolUses) > 0 || recovered {
			consecutiveTextOnly = 0
		}

		// materializeFailureMsg: fed back as tool_result (isError: true) on failure; loop continues for correction.
		var materializeFailureMsg string
		if proposeInput != nil {
			result, err := decodeProposeInput(proposeInput)
			if err != nil {
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
				result.CostUSD = cost.value()
				result.ExplorationToolCalls = explorationToolCalls
				result.ModelID, result.Effort = choice.ModelID, choice.Effort
				// A recovered call has no tool_use ID to pair a resumed tool_result with, so repair uses the flat-recap fallback.
				if !recovered {
					result.conversation = newConversation(messages, message, proposeID, choice)
				}
				return &result, nil
			}
			slog.Warn("ai: propose_changes edit materialization failed, retrying", "iteration", iteration, "recovered_from_text", recovered)
			materializeFailureMsg = retryMsg
		}

		if recovered {
			messages = append(messages, message.ToParam())
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(recoveredMaterializeFailure(materializeFailureMsg))))
			continue
		}

		if len(toolUses) == 0 {
			// Never turn the text into an answer: that would need answered_question: true, which bypasses the
			// fake-success guard, so a plain-text "Done!" would reach the merchant with nothing changed.
			consecutiveTextOnly++
			if consecutiveTextOnly >= maxConsecutiveTextOnlyRounds {
				slog.Warn("ai: model stuck replying in text, stopping", "iteration", iteration, "consecutive_text_rounds", consecutiveTextOnly)
				return nil, errStuckInTextReplies
			}
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
				resultBlocks = append(resultBlocks, toolResultBlock(tu.ID, materializeFailureMsg, true))
				continue
			}
			// Enforced here, not by the request: DeepSeek calls tools a round doesn't allow (seen on forced rounds).
			if !toolOffered(allowedTools, tu.Name) {
				slog.Warn("ai: model called a tool this round doesn't allow; not running it", "iteration", iteration, "tool", tu.Name)
				resultBlocks = append(resultBlocks, toolResultBlock(tu.ID, toolUnavailableMessage(tu.Name, allowedTools), true))
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
			resultBlocks = append(resultBlocks, toolResultBlock(tu.ID, output, isError))
		}
		messages = append(messages, anthropic.NewUserMessage(resultBlocks...))
	}

	// Every round from forceProposeAfterRounds on was forced, so the model had its chance; the merchant gets a fixed
	// question instead of an error, and never the model's own text, which could claim a fix that wasn't made.
	slog.Warn("ai: no proposal after forced rounds; ending the turn with a clarifying question", "iterations", maxToolIterations)
	return &Result{
		Summary:              ExhaustedSearchReply,
		NeedsClarification:   true,
		InputTokens:          totalInputTokens,
		OutputTokens:         totalOutputTokens,
		CostUSD:              cost.value(),
		ExplorationToolCalls: explorationToolCalls,
		ModelID:              choice.ModelID,
		Effort:               choice.Effort,
	}, nil
}

// toolOffered reports whether name is one of the tools this round allows.
func toolOffered(tools []anthropic.ToolUnionParam, name string) bool {
	for _, t := range tools {
		if t.OfTool != nil && t.OfTool.Name == name {
			return true
		}
	}
	return false
}

func toolUnavailableMessage(name string, tools []anthropic.ToolUnionParam) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.OfTool != nil {
			names = append(names, t.OfTool.Name)
		}
	}
	return fmt.Sprintf("%s isn't allowed in this round, so it wasn't run. Allowed now: %s.", name, strings.Join(names, ", "))
}

// toolResultBlock prefixes failures with "ERROR:" and keeps is_error: DeepSeek ignores is_error, so the flag alone is invisible.
func toolResultBlock(toolUseID, text string, isError bool) anthropic.ContentBlockParamUnion {
	if isError {
		text = "ERROR: " + text
	}
	return anthropic.NewToolResultBlock(toolUseID, text, isError)
}

// freshMessages builds a first call's messages from flat history; must stay byte-identical (prefix caching).
func freshMessages(history []Turn, prompt string, images []Image) []anthropic.MessageParam {
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
	return messages
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

// warnReadBeforeWriteViolations: logs every action:"update" not in knownPaths (detection only, no b...
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

	entry, client, reqOpts, err := g.model(g.catalog.Summary())
	if err != nil {
		return "", err
	}
	params := anthropic.MessageNewParams{
		Model:     entry.Model,
		MaxTokens: summarizeMaxTokens,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(instruction))},
		// Explicit: DeepSeek thinks by default, which would eat the 1024-token budget.
		Thinking: anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
	}
	message, err := client.Messages.New(ctx, params, reqOpts...)
	if err != nil {
		alertProviderError(err, entry.Model)
		return "", fmt.Errorf("summarize turns: %w", err)
	}
	return textOnly(*message), nil
}

// textOnly ignores thinking blocks; the summary is replayed as history, so reasoning must never leak into it.
func textOnly(message anthropic.Message) string {
	var text strings.Builder
	for _, block := range message.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String()
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
    final set of changes for this request — not a partial draft.
11. In files you can only create or write %s — never an image (.svg, .png, .jpg, ...) or any other
    file type, even though a theme may contain them. You can never create an image file — except an
    attached image the merchant asks you to use. Declare it in use_attachments with a path under
    images/, then reference it with {{ 'images/<name>' | asset_url }}. Only do that when the merchant's
    own words ask you to use, place, add or put the image; an image sent as a look/style reference
    ("make it look like this") is not a placement. Otherwise reference an existing image, or put SVG
    inline in a .liquid file.`, themeEngineSpec, themefs.GeneratedFileTypes()),
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
%s%s`, tc.ThemeSlug, mode, modeRestrictionNote(mode), pagesJSON, defaultsJSON, formatFileTree(tc.FileTree), formatManifest(tc.Manifest),
		formatDraftPaths(tc.DraftPaths, tc.StagedImagePaths))
}

const draftUndoRule = "Never undo earlier changes by rewriting a file — you can't see the originals. " +
	"If earlier changes should go, tell the merchant to use Undo on that turn."

// formatDraftPaths lists unsaved-draft paths, sorted so identical drafts give identical prompts; "" when there are none.
func formatDraftPaths(paths []string, stagedImages map[string]bool) string {
	if len(paths) == 0 {
		return ""
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("- Files with unsaved changes from earlier turns (the merchant hasn't applied them yet — keep that work; change only what this request needs):\n")
	// At the decision point because the spec's version of this rule didn't hold against repeated failed fixes.
	b.WriteString("  " + draftUndoRule + "\n")
	for _, p := range sorted {
		if stagedImages[p] {
			fmt.Fprintf(&b, "  - %s (staged image — goes live when the merchant applies)\n", p)
			continue
		}
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	return b.String()
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
