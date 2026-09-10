package themebuild

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/safego"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
	"ai-chat/internal/urlfetch"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

// loadThemeFilesConcurrency bounds how many concurrent ReadFile calls
// LoadThemeFiles makes — generous enough to turn a few dozen sequential
// ~50ms reads into a fraction of a second, not so high it looks like a
// burst to flowpos-backend or exhausts this process's own outbound
// connection pool.
const loadThemeFilesConcurrency = 8

const (
	pathPagesJSON   = "pages.json"
	pathLayoutStart = "liquid/layout-start.liquid"
	pathLayoutEnd   = "liquid/layout-end.liquid"

	// ChatType is the chat.Chat "type" this module owns — the chat package
	// itself is generic (see its doc comment); "builder" is what scopes a
	// tenant's theme-builder thread apart from any future, unrelated chat
	// use case sharing the same tenant.
	ChatType = "builder"

	// maxThemeCheckRetries bounds how many times a proposal themecheck
	// rejects is sent back to the model with its findings before doGenerate
	// gives up — up to maxThemeCheckRetries+1 total Generate calls (the
	// original attempt plus this many retries).
	maxThemeCheckRetries = 2

	// maxImagesPerMessage bounds how many images one prompt can attach —
	// see the image-attachment feature. Enforced here (not just the
	// frontend) since a client-side cap alone is trivially bypassable by
	// anyone calling this API directly.
	maxImagesPerMessage = 5

	// MaxImageAttachmentBytes bounds one attached image's decoded size —
	// exported (all attachment size/validation logic lives here in the
	// service, not the HTTP handler — see the image/HTML-attachment
	// features' own doc comments) so the handler never needs its own
	// mirrored literal. The same 5MB tenant-dashboard already enforces
	// client-side for its own image uploads (MAX_IMAGE_UPLOAD_BYTES) — a
	// client-side cap alone is trivially bypassable by anyone calling this
	// API directly, so this is the real enforcement.
	MaxImageAttachmentBytes = 5 * 1024 * 1024

	// MaxHTMLUploadBytes is what's accepted on the wire, BEFORE
	// SanitizeHTMLAttachment strips it — generous, since a real "save page
	// as HTML" export commonly embeds every image as a giant inline base64
	// data: URI, which stripping removes entirely. MaxHTMLAttachmentBytes
	// below is the real, much tighter cap that applies AFTER stripping,
	// since that's what actually reaches the model as prompt text.
	MaxHTMLUploadBytes = 5 * 1024 * 1024

	// MaxHTMLAttachmentBytes bounds one attached reference HTML file's
	// content AFTER SanitizeHTMLAttachment strips scripts and inline
	// base64 assets. Far tighter than an image's cost: raw text tokens
	// cost roughly 1 per ~4 characters, unlike an image's flat per-image
	// token cost, so this can't be anywhere near the image size cap
	// without risking a very expensive single turn — ~300KB is generous
	// for real markup/CSS once embedded assets are gone, while staying
	// well under maxTokens' own headroom once combined with the rest of a
	// turn's prompt/system/tool-loop budget.
	MaxHTMLAttachmentBytes = 300_000

	pathDefaultsJSON = "defaults.json"
)

// attachmentKindLimit is one kind's count/size caps — see attachmentLimits.
// PostStripMaxBytes is zero for a kind with no post-sanitize pass (today,
// only HTML has one: SanitizeHTMLAttachment). Both byte fields are decoded/
// raw-byte sizes, never base64 length — MaxImageAttachmentBytes is checked
// via base64.StdEncoding.DecodedLen (the wire value is base64, the check
// isn't), and MaxHTMLUploadBytes/MaxHTMLAttachmentBytes were already
// decoded-size checks even before this restructuring: HTMLAttachmentContent
// is plain UTF-8 text on the wire, never base64, so len() on it already
// measured raw bytes.
type attachmentKindLimit struct {
	MaxCount          int
	MaxBytes          int64
	PostStripMaxBytes int64
}

// attachmentLimits keys maxImagesPerMessage/MaxImageAttachmentBytes/
// MaxHTMLUploadBytes/MaxHTMLAttachmentBytes by chat.AttachmentKind — adding
// a new kind's count/size caps (e.g. PDF) is one more map entry, not a new
// exported const plus a new `if` in Generate. The numbers and their
// reasoning are unchanged from before this restructuring — see each const's
// own doc comment above; this just gives Generate one place to look them up
// by kind instead of a literal per attachment type.
var attachmentLimits = map[chat.AttachmentKind]attachmentKindLimit{
	chat.AttachmentKindImage: {
		MaxCount: maxImagesPerMessage,
		MaxBytes: MaxImageAttachmentBytes,
	},
	chat.AttachmentKindHTML: {
		MaxCount:          1,
		MaxBytes:          MaxHTMLUploadBytes,
		PostStripMaxBytes: MaxHTMLAttachmentBytes,
	},
}

// generateTimeoutNanos backs generateTimeout()/setGenerateTimeoutForTest —
// an atomic.Int64 (nanoseconds), not a plain time.Duration var: production
// code never writes it, but TestRunGeneration_EachIterationGetsFreshTimeout
// does, from the test goroutine, concurrently with background drain-loop
// goroutines reading it (see runOneQueuedGeneration) — a plain var would be
// a genuine, race-detector-flagged data race between the two, even though
// in practice the write always happens well outside any window a
// background goroutine is reading it.
var generateTimeoutNanos = func() *atomic.Int64 {
	var v atomic.Int64
	// 65 minutes: not unbounded, but generous enough for a full-site
	// redesign at high effort, which can legitimately run close to an hour.
	// Also reused as the staleness threshold for reaping abandoned
	// "in progress" rows (see ReapStaleGenerations) — raising this means a
	// truly stuck generation stays marked in-progress that much longer
	// before being cleaned up.
	v.Store(int64(65 * time.Minute))
	return &v
}()

// generateTimeout bounds one drain-loop iteration's background work — see
// runOneQueuedGeneration, which gives every queued generation its own fresh
// context.WithTimeout(ctx, generateTimeout()) rather than sharing one
// budget across a whole queue.
func generateTimeout() time.Duration { return time.Duration(generateTimeoutNanos.Load()) }

// heartbeatTickerNanos backs runOneQueuedGeneration's heartbeat ticker
// interval — same atomic.Int64 reasoning as generateTimeoutNanos above:
// production never writes it, but a test needs to shrink it far below the
// real-world 30s (matching heartbeatThrottle — see generation_events.go) so
// it doesn't have to wait 30 real seconds for a tick, while a concurrent
// generation's own ticker goroutine (started on a background drain-loop
// goroutine, not the test's) may be reading it at the same time.
var heartbeatTickerNanos = func() *atomic.Int64 {
	var v atomic.Int64
	v.Store(int64(heartbeatThrottle))
	return &v
}()

func heartbeatTickerInterval() time.Duration { return time.Duration(heartbeatTickerNanos.Load()) }

// generator is the subset of *ai.Generator's behavior Service depends on —
// letting tests substitute a fake that never calls the real Claude API,
// which matters most for checkAndRepair's retry loop (multiple Generate
// calls per turn). *ai.Generator satisfies this today with no changes on
// its side; callers passing one continue to work unchanged.
type generator interface {
	Generate(ctx context.Context, tc ai.ThemeContext, history []ai.Turn, prompt string, images []ai.Image, onDelta func(string), progress ai.ToolProgress, toolExec ai.ToolExecutor, readFile ai.FileReader) (*ai.Result, error)
	// SupportsVision reports whether this generator was configured with a
	// vision-capable model — see Generate's own guard using this, which
	// rejects an image-bearing prompt up front rather than persisting an
	// image nothing downstream can ever process.
	SupportsVision() bool
	// Summarize is used by summarizeOldTurns to collapse old chat history
	// into one synthetic turn instead of resending it verbatim on every
	// call — see summarizeOldTurns's doc comment. *ai.Generator's fake mode
	// (see ai.NewFake) implements this with a cheap deterministic string
	// and never calls the real API, matching Generate's own fake-mode
	// convention.
	Summarize(ctx context.Context, turns []ai.Turn) (string, error)
}

// linkFetcher matches *urlfetch.Fetcher's own methods — a private
// interface (same pattern as generator above) so Generate/doGenerate can
// be tested against a fake that never makes a real network call, and so
// this package doesn't need to import urlfetch's concrete type anywhere
// but NewService. FetchStylesheets was added alongside Fetch once
// Service.fetchReferenceURL started building a digest instead of just
// sanitizing raw HTML — see its own doc comment.
type linkFetcher interface {
	Fetch(ctx context.Context, rawURL string, maxBytes int64) (urlfetch.Result, error)
	FetchStylesheets(ctx context.Context, htmlSrc string, finalURL *url.URL) (css string, count int)
}

// Service is the AI theme builder's orchestration: turn a prompt into
// proposed changes and stage them into the chat's draft overlay (see
// Generate) — writing to the real theme is a separate, explicit ApplyDraft
// step (see this package's own doc comment for the draft/apply split).
type Service struct {
	repo  *Repository
	chats *chat.Service
	gen   generator
	// links fetches a merchant-pasted reference URL's HTML — see the
	// link-reference feature in Generate. nil in tests that construct a
	// Service by struct literal without setting it (see e.g.
	// generate_valid_proposal_test.go); Generate treats that the same way
	// it already treats a turn with no reference link at all, so those
	// tests need no changes.
	links linkFetcher
	// linkCache is a short-TTL cache in front of links.Fetch (see
	// fetchReferenceURL and referenceURLCache's own doc comment) — nil in
	// the same struct-literal tests links itself can be nil in;
	// fetchReferenceURL falls back to an uncached call when either is nil.
	linkCache *referenceURLCache
	// store is always the REAL (non-overlay) store — see doGenerate, which
	// wraps it in a fresh themefs.OverlayStore per generation call rather
	// than mutating this field. A mutable "current store" field here would
	// be a data race: this Service is shared across every concurrent
	// generation for every chat/tenant, and each one's draft is its own.
	store      themefs.ThemeStore
	themeLocks themeLocker // redisThemeLock if REDIS_URL was configured, keyedMutex otherwise — see themelock.go
	bus        eventBus    // redisEventBus if REDIS_URL was configured, inProcessEventBus otherwise — see eventEmitter
	tokens     *pendingTokens
	// historySummarizationEnabled/historySummaries/historySummaryLocks back
	// summarizeOldTurnsCached (see history_summary.go for the full
	// rationale) — always non-nil/true after NewService; not a constructor
	// parameter because NewService's 5-arg shape is depended on by every
	// test in this package and several in internal/server/handlers, for a
	// value that in practice never varies across the single Service
	// instance this process ever builds. Overridden via
	// SetHistorySummarizationEnabled, called once by server.go's wiring.
	historySummarizationEnabled bool
	historySummaries            *historySummaryCache
	historySummaryLocks         *stripedMutex
}

// SetHistorySummarizationEnabled overrides the default (enabled) — see the
// Service struct's own doc comment on why this isn't a NewService
// parameter. Call once, before serving traffic; not safe to call
// concurrently with a generation already reading the field.
func (s *Service) SetHistorySummarizationEnabled(enabled bool) {
	s.historySummarizationEnabled = enabled
}

// NewService wires the service's dependencies. rdb may be nil (see
// NewRedisClient) — generation events are then still durably written to
// generation_events, and live delivery falls back to an in-process fan-out
// (see eventbus.go) that only reaches a WebSocket connected to this same
// replica. store takes the themefs.ThemeStore interface, not the concrete
// *themefs.Store, purely so tests can substitute a fake — server.go's own
// wiring still always passes a real *themefs.Store.
func NewService(repo *Repository, chats *chat.Service, gen *ai.Generator, store themefs.ThemeStore, rdb *redis.Client) *Service {
	var bus eventBus
	var locks themeLocker
	if rdb != nil {
		bus = newRedisEventBus(rdb)
		locks = newRedisThemeLock(rdb)
	} else {
		bus = newInProcessEventBus()
		// In-process locking only serializes staging/apply/revert within
		// THIS replica — two replicas can still race the same theme_slug.
		// Same degradation this service already accepts for the event bus
		// when REDIS_URL is unset (see the warning above it in server.go):
		// tolerable for a single-replica deployment, not for more than one.
		slog.Warn("REDIS_URL is not set — theme write locking falls back to a single-replica, in-process lock, " +
			"which does not prevent two replicas from staging/applying/reverting the same theme concurrently")
		locks = newKeyedMutex()
	}
	return &Service{
		repo:                        repo,
		chats:                       chats,
		gen:                         gen,
		links:                       urlfetch.NewFetcher(),
		linkCache:                   newReferenceURLCache(),
		store:                       store,
		themeLocks:                  locks,
		bus:                         bus,
		tokens:                      newPendingTokens(),
		historySummarizationEnabled: true,
		historySummaries:            newHistorySummaryCache(),
		historySummaryLocks:         newStripedMutex(historySummaryLockStripes),
	}
}

// pendingTokens holds each queued generation's bearer token in memory only,
// keyed by generation ID — never in the generations table (see the
// 20260812000001 migration's doc comment): a bearer token in a DB column is
// a credential-at-rest problem, and a prompt queued behind several others
// may not run for many minutes, by which time flowpos-backend may no longer
// accept it anyway.
//
// Keyed by generation ID rather than owned by whichever goroutine happens
// to call DequeueNext: two requests racing to claim an empty running slot
// (see Generate) can result in either one's DequeueNext call promoting
// *either* request's own enqueued row — the token has to travel with the
// row that actually gets promoted, not with whichever caller won the race
// to promote something. store is called once per accepted prompt (Generate);
// take is called once per drain-loop iteration (runGeneration) and removes
// the entry — a queued generation only ever runs once, a failure is never
// retried (see runOneQueuedGeneration), so there is nothing to keep it
// around for afterward.
//
// On a pod restart this map is empty. A queued row that survives in the
// database (queued rows are just data) has no entry here anymore — take
// reports that exactly like an expired token, which is the correct
// treatment: see runOneQueuedGeneration and reapOrphanedQueues, which hits
// the identical "no token" path for the same reason after a crash.
type pendingTokens struct {
	mu     sync.Mutex
	tokens map[string]string
}

func newPendingTokens() *pendingTokens {
	return &pendingTokens{tokens: make(map[string]string)}
}

func (p *pendingTokens) store(generationID, token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens[generationID] = token
}

// take returns generationID's token and whether one was found, removing it
// either way.
func (p *pendingTokens) take(generationID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	token, ok := p.tokens[generationID]
	delete(p.tokens, generationID)
	return token, ok
}

// discard drops generationID's token without returning it — used when a
// queued generation is cancelled (see QueueService.Cancel) before it ever
// gets a chance to run, so the map doesn't hold a stale entry until process
// exit.
func (p *pendingTokens) discard(generationID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tokens, generationID)
}

// ErrVisionNotConfigured means a prompt attached an image but this
// deployment has no vision-capable model configured (DEEPSEEK_VISION_MODEL/
// ANTHROPIC_VISION_MODEL) — rejected up front, before RecordUserMessage
// ever persists an image nothing could process.
var ErrVisionNotConfigured = errors.New("image attachments aren't enabled on this deployment")

// ErrTooManyImages means a prompt attached more than maxImagesPerMessage
// images.
var ErrTooManyImages = errors.New("too many images attached")

// ErrImageTooLarge means one attached image's decoded size exceeded
// MaxImageAttachmentBytes.
var ErrImageTooLarge = errors.New("an attached image is too large")

// ErrHTMLAttachmentTooLarge means the attached HTML file's content
// exceeded MaxHTMLUploadBytes (raw) or MaxHTMLAttachmentBytes (post-strip).
var ErrHTMLAttachmentTooLarge = errors.New("attached HTML file is too large")

// ErrLinkFetchFailed means a reference URL the merchant pasted directly in
// their prompt (see the link-reference feature in Generate) couldn't be
// used — wraps the underlying urlfetch error (invalid URL, blocked/private
// host, unreachable, or wrong content type) for detail; every one of those
// is already merchant-readable on its own (see urlfetch's own sentinel
// errors), so nothing here needs to redact or re-explain it. A URL past
// MaxHTMLUploadBytes is truncated rather than rejected (see urlfetch.Fetch),
// so "too large" is no longer one of the reasons this wraps.
var ErrLinkFetchFailed = errors.New("could not use the link in your message as a reference")

// ErrGenerationInProgress means the tenant's chat already has a background
// generation running — see the generations table (phase 3a) and
// Repository.StartGeneration/DequeueNext. Generate itself never returns
// this anymore: a prompt that can't run immediately is queued instead of
// rejected (see Generate's doc comment). It's now purely an internal signal
// between DequeueNext and its callers (Generate, runGeneration,
// reapOrphanedQueues) for "something is already running, this dequeue
// attempt legitimately lost the race" — never surfaced to a caller as an
// error to react to.
var ErrGenerationInProgress = errors.New("a generation is already in progress for this chat")

// GenerateInput is one merchant prompt, always against the tenant's one
// ongoing "builder" chat (see chat.Service.GetOrCreateChat).
type GenerateInput struct {
	TenantID  uint64
	UserID    *uint64
	UserName  string
	UserEmail string
	// Token is the caller's own bearer token, forwarded to flowpos-backend's
	// theme-file API (see internal/themefs.Store) so every read/write acts
	// as this same user, subject to flowpos-backend's own ownership checks.
	Token     string
	ThemeSlug string
	Prompt    string
	// Images, when non-empty (capped at maxImagesPerMessage), attaches one
	// or more images to this turn's prompt — see the image-attachment
	// feature. Only ever sent to the model for turns processed as part of
	// THIS generation call (the initial attempt and any invalid-proposal/
	// repair retries within it, which each call Generate fresh — see
	// generateValidProposal/checkAndRepair); never resurfaced on a later,
	// separate prompt.
	Images []chat.MessageImage
	// HTMLAttachmentFilename/HTMLAttachmentContent, when both set, attach
	// one reference HTML file to this turn's prompt — see the
	// HTML-attachment feature. Folded into the effective prompt text at
	// each Generate call within this turn (see promptWithHTMLAttachment)
	// rather than sent as a separate structured param, since it's plain
	// text — no vision-model plumbing needed for it. Unlike Images (which
	// really is only ever this one call), doGenerate can also populate
	// these two from an EARLIER turn's attachment when the current turn
	// has none of its own — see findCarryForwardSourceMessageID and
	// HTMLAttachmentCarriedForward below.
	HTMLAttachmentFilename *string
	HTMLAttachmentContent  *string
	// HTMLAttachmentIsExternalLink is true when HTMLAttachmentContent came
	// from a URL fetch (see the link-reference feature) rather than a file
	// the merchant uploaded — true whether that fetch happened on THIS turn
	// or an earlier one it was carried forward from (see
	// HTMLAttachmentCarriedForward). promptWithHTMLAttachment uses it to
	// call out explicitly that the content is a completely different,
	// external website — not the merchant's own theme — since a fetched
	// competitor/inspiration site's markup routinely contains the same kind
	// of e-commerce shapes (add-to-cart buttons, product data attributes)
	// the merchant's own theme does, and without this the model has been
	// observed going to grep the merchant's own theme files for matching
	// patterns even for a plain read-only question about the fetched site.
	// Restored from storage via looksLikeFetchedLink — chat_message_attachments
	// has no column of its own for this (see that function's own doc
	// comment for why the filename alone is a reliable enough signal).
	HTMLAttachmentIsExternalLink bool
	// HTMLAttachmentCarriedForward is true when HTMLAttachmentFilename/
	// Content came from an earlier turn in this chat (see
	// findCarryForwardSourceMessageID), not from the current turn's own
	// message. promptWithHTMLAttachment uses it to tell the model the
	// reference won't be mentioned again in the merchant's latest message
	// but is still the active one — without this, the model has no way to
	// know the attachment below wasn't just silently dropped.
	HTMLAttachmentCarriedForward bool
	// HTMLAttachmentTruncated is true when a link-fetched attachment had to
	// be cut short — either the raw HTML fetch itself (see urlfetch.Result.
	// Truncated) or the built digest exceeding its own hard cap (see
	// urlfetch.Digest.Truncated); PostStripMaxBytes truncation is a third,
	// now-effectively-unreachable backstop (see Service.fetchReferenceURL's
	// own doc comment) kept only for a future change to either constant.
	// Unlike an uploaded file, which is still hard-rejected over its own
	// limit — the merchant controls what they upload, not how heavy someone
	// else's homepage is. promptWithHTMLAttachment states this in the
	// framing so the model doesn't read a missing footer/section as absent
	// from the real page — it's just past where this turn's copy was cut.
	HTMLAttachmentTruncated bool
	// ReferenceURL is a URL Generate found in Prompt (see
	// urlfetch.ExtractReferenceURL) and validated the shape of, but has not
	// fetched — carried through the queue via Generation.ReferenceURL
	// exactly like Prompt itself, since runOneQueuedGeneration rebuilds
	// GenerateInput fresh from that row on every dequeue. doGenerate does
	// the actual fetch (see its own reference-URL block) and, on success,
	// populates HTMLAttachmentFilename/Content/IsExternalLink from it —
	// this field itself is never read past that point in the same turn.
	ReferenceURL string
	// ReferenceURLFetchFailed is set by doGenerate when ReferenceURL was
	// present but the fetch itself failed — network error, blocked host,
	// non-HTML response, etc. (never a malformed-URL case; Generate already
	// rejects that synchronously before ReferenceURL is ever set). Unlike
	// every other HTML-attachment failure in this service, this one must
	// NOT fail the turn: the merchant asked a real question and deserves an
	// answer about everything except the page. promptWithHTMLAttachment
	// uses this to tell the model plainly that the fetch failed, instead of
	// silently proceeding as if no link had ever been mentioned.
	ReferenceURLFetchFailed bool
	// ReferenceURLBlocked is set alongside ReferenceURLFetchFailed
	// specifically when the failure was urlfetch.ErrBlocked (the site
	// itself refused the request — a 401/403/429 — rather than being
	// unreachable) — promptWithHTMLAttachment uses it to give the model
	// the actionable version of the note ("ask the merchant to paste the
	// HTML instead") rather than a generic "couldn't reach it."
	ReferenceURLBlocked bool
	// ReferenceURLEmptyAfterSanitize is set alongside ReferenceURLFetchFailed
	// when the fetch itself succeeded but the built digest came back Empty
	// (see urlfetch.Digest.Empty — no headings, no landmarks, no real
	// copy) — a client-rendered page (React/Vue/etc.) whose server-sent
	// HTML is just an empty mount point with its real content injected by
	// scripts digests to exactly this. Treated as a failure, not a success
	// with an empty attachment: without this, doGenerate would hand the
	// model an empty HTMLAttachmentContent alongside the external-link
	// success framing, which had the model assert it read a page it has
	// nothing from. promptWithHTMLAttachment gives this its own distinct
	// note rather than the generic "couldn't reach it" one. (Field name
	// kept from when this was measured on SanitizeHTMLAttachment's output —
	// renaming it now would touch every caller for no behavioral change.)
	ReferenceURLEmptyAfterSanitize bool
	// UserMessageID is set by Generate right after RecordUserMessage and
	// carried through the queue (Generation.UserMessageID) so doGenerate
	// can re-resolve Images from chat_messages once this turn actually
	// runs — runOneQueuedGeneration rebuilds GenerateInput fresh from the
	// generations row on every dequeue, so Images set here don't otherwise
	// survive that rebuild. See doGenerate.
	UserMessageID *string
	// Mode restricts what this one turn may touch — see the
	// ai.GenerationMode* constants. Empty (the default, and what every
	// caller sends today) behaves as ai.GenerationModeEdit: the full
	// read/write tool loop, no restriction. This must be explicit, set only
	// by a caller deliberately running the guided "start a theme from
	// scratch" flow (turn 1 brand-only, turn 2 copy-only) — inferring it
	// from the chat's turn count instead was tried and reverted: a chat's
	// turn count says nothing about whether this is a fresh onboarding
	// sequence or an ordinary chat on an already-established theme, and
	// forcing the latter's first two turns into brand/copy-only mode is a
	// regression, not a feature (it silently refuses everyday requests like
	// "create a page").
	Mode string
}

// GenerateOutcome is the immediate (synchronous) result of accepting a
// prompt: the chat, the user's own recorded message, and where this
// prompt's generation landed in line. AssistantMessage and Files are always
// nil here — Generate now returns as soon as the prompt is recorded and
// either kicked off or queued (see Generate's doc comment), not once Claude
// has actually replied. The real outcome (a new assistant message,
// generated files, or an error) arrives later — the caller polls GET
// /chat, which reports the pending queue (see ListPending) and surfaces the
// new history once each turn is done.
type GenerateOutcome struct {
	Chat             chat.Chat
	UserMessage      chat.Message
	AssistantMessage *chat.Message
	Files            []GeneratedFile
	// QueuePosition is how many generations (running + queued) were ahead
	// of this one at the moment it was accepted — 0 means it was dequeued
	// immediately and is the one running now.
	QueuePosition int
	// GenerationID is the row this prompt is tracked under — the caller
	// needs it to cancel a queued prompt (DELETE /chats/:chatId/queue/
	// :generationId) or to correlate it with the "queued"/"dequeued"/
	// "done"/"failed" events it'll see on the stream.
	GenerationID string
}

// Generate resolves (or creates) the chat, records the prompt, and returns
// immediately — the actual Claude call, proposal validation, and (if the
// model proposed changes) staging them into the chat's draft overlay all
// happen in a background goroutine (see runGeneration), not before this
// returns.
//
// This is deliberately async, not a synchronous call the client awaits:
// a full generation can legitimately take several minutes, and no
// intermediary in a real deployment — a CDN proxy, a corporate firewall, a
// flaky mobile connection, even the browser backgrounding the tab — can be
// trusted to keep one HTTP request alive that long. Every request this
// service handles now finishes in milliseconds; the caller learns the
// actual result by polling GET /chat's `queue` field instead of waiting on
// this call's response.
//
// Prompts queue and run one at a time, in order, never in parallel: a
// second prompt usually depends on the first's result ("now make that
// header blue"), and themeLocks/uniq_generations_running_chat both exist
// specifically to prevent two writers touching the same theme at once. So
// this never returns ErrGenerationInProgress anymore — a prompt that can't
// run immediately is queued instead of rejected:
//
//  1. Record the user's message immediately, unconditionally — the
//     merchant's prompt appears in the transcript the moment they hit send,
//     whether or not it runs right now. This is the reverse of the old
//     ordering (record-then-claim used to be claim-then-record): enqueueing
//     below can't fail with "already running" the way StartGeneration used
//     to, so there's no slot to release if RecordUserMessage had come first
//     and something after it failed.
//  2. Enqueue a "queued" row carrying everything a later, detached
//     runGeneration call will need to actually run this turn (see the
//     Generation struct) — everything except the bearer token, which is
//     kept in memory only (see pendingTokens).
//  3. Try to immediately dequeue the oldest pending row for this chat. If
//     that succeeds, this prompt (or, in a rare race with another request
//     for the same chat, whichever prompt actually was oldest) starts
//     running right now. If something is already running, this prompt just
//     waits — the generation currently running will dequeue it in turn once
//     it finishes (see runGeneration's drain loop), no extra work needed
//     here.
//
// A model/infra failure, a rejected proposal, or a failure while staging
// changes into the draft is recorded as a failed chat turn (see doGenerate's
// own defer and chat.MessageStatusFailed's doc comment), so the transcript
// itself shows something went wrong — unlike before this became async, when
// errors were purely request-scoped and never touched chat history. The
// generations table (phase 3a) tracks the same failure independently,
// feeding GenerationStatus rather than the transcript.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (GenerateOutcome, error) {
	if in.ThemeSlug == "" {
		return GenerateOutcome{}, errors.New("theme_slug is required")
	}
	imageLimit := attachmentLimits[chat.AttachmentKindImage]
	if len(in.Images) > imageLimit.MaxCount {
		return GenerateOutcome{}, fmt.Errorf("%w: at most %d images per message", ErrTooManyImages, imageLimit.MaxCount)
	}
	// Reject before ever persisting an image nothing downstream can
	// process — cheaper and clearer than letting it fail deep inside
	// doGenerate once this turn is dequeued.
	if len(in.Images) > 0 && !s.gen.SupportsVision() {
		return GenerateOutcome{}, ErrVisionNotConfigured
	}
	// DecodedLen is pure arithmetic on the base64 string's own length — no
	// need to actually decode just to measure size (the HTTP handler
	// already validated each Base64 field really is valid base64 at bind
	// time; this only needs the size).
	for i, img := range in.Images {
		if int64(base64.StdEncoding.DecodedLen(len(img.Base64))) > imageLimit.MaxBytes {
			return GenerateOutcome{}, fmt.Errorf("%w: image %d", ErrImageTooLarge, i)
		}
	}
	// A merchant pasting a bare reference link directly in their prompt
	// ("https://example.com can you access this link") gets treated almost
	// exactly like uploading that URL's page as an HTML attachment — but
	// the actual fetch does NOT happen here. This method backs POST
	// /chats/messages, which cmd/server/main.go documents as never doing
	// slow synchronous work; an outbound HTTP call (urlfetch.fetchTimeout
	// is 10s) blocking the request, and a fetch failure returning before
	// RecordUserMessage even runs (silently dropping the merchant's prompt
	// from the transcript, unlike every other failure this service
	// records), both violate that. So this only ever DETECTS a URL and
	// validates its shape — genuinely malformed input (ValidateURL) is the
	// one case a synchronous 4xx is still correct, since it needs no
	// network — and carries the URL on the enqueued Generation row.
	// doGenerate does the real fetch once this turn is actually dequeued
	// (see its own reference-URL block). Only runs when no HTML file was
	// explicitly uploaded this turn — an upload is a more deliberate
	// signal than a URL that merely appears somewhere in the prompt text,
	// so it always wins over a link mentioned in passing.
	//
	// ExtractReferenceURL, not the looser ExtractFirstURL: any URL
	// anywhere in the prompt used to count, which meant "our shop is at
	// https://example.com — make the header blue" fetched a whole
	// unrelated page and injected external-link framing into a request
	// that had nothing to do with it. See ExtractReferenceURL's own doc
	// comment for the intent check it applies instead.
	var referenceURL string
	if in.HTMLAttachmentContent == nil {
		if link, ok := urlfetch.ExtractReferenceURL(in.Prompt); ok {
			if _, verr := urlfetch.ValidateURL(link); verr != nil {
				return GenerateOutcome{}, fmt.Errorf("%w: %s", ErrLinkFetchFailed, verr.Error())
			}
			referenceURL = link
		}
	}

	if in.HTMLAttachmentContent != nil {
		htmlLimit := attachmentLimits[chat.AttachmentKindHTML]
		if int64(len(*in.HTMLAttachmentContent)) > htmlLimit.MaxBytes {
			return GenerateOutcome{}, fmt.Errorf("%w: attached HTML file is over %d bytes", ErrHTMLAttachmentTooLarge, htmlLimit.MaxBytes)
		}
		sanitized := SanitizeHTMLAttachment(*in.HTMLAttachmentContent)
		if int64(len(sanitized)) > htmlLimit.PostStripMaxBytes {
			return GenerateOutcome{}, fmt.Errorf(
				"%w: still over %d bytes after removing embedded images/scripts",
				ErrHTMLAttachmentTooLarge, htmlLimit.PostStripMaxBytes)
		}
		in.HTMLAttachmentContent = &sanitized
	}

	c, err := s.chats.GetOrCreateChat(ctx, in.TenantID, ChatType)
	if err != nil {
		return GenerateOutcome{}, err
	}

	userMsg, err := s.chats.RecordUserMessage(ctx, c, in.UserID, in.UserName, in.UserEmail, in.Prompt, in.Images, in.HTMLAttachmentFilename, in.HTMLAttachmentContent)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("record user message: %w", err)
	}
	in.UserMessageID = &userMsg.ID

	genID := uuid.NewString()
	position, err := s.repo.EnqueueGeneration(ctx, Generation{
		ID:            genID,
		ChatID:        c.ID,
		TenantID:      in.TenantID,
		Prompt:        in.Prompt,
		ReferenceURL:  referenceURL,
		UserMessageID: &userMsg.ID,
		ThemeSlug:     in.ThemeSlug,
		Mode:          in.Mode,
	})
	if err != nil {
		if errors.Is(err, ErrQueueFull) {
			return GenerateOutcome{}, ErrQueueFull
		}
		return GenerateOutcome{}, fmt.Errorf("enqueue generation: %w", err)
	}

	// Held in memory only — see pendingTokens' doc comment for why this
	// never becomes a column on the row EnqueueGeneration just inserted.
	s.tokens.store(genID, in.Token)

	next, err := s.repo.DequeueNext(ctx, c.ID)
	switch {
	case err == nil:
		// Detached from the caller's own request lifecycle (which is about
		// to end the moment this function returns) but not unbounded: each
		// drain-loop iteration gets its own generateTimeout budget (see
		// runGeneration), matching the HTTP server's own writeTimeout.
		go func() {
			// One-shot: a panic here ends just this generation's run — the
			// reaper's own orphaned-queue sweep independently recovers a
			// generation that never finished, so this doesn't need to keep
			// retrying itself. See safego's package doc comment on why this
			// is needed at all: gin.Recovery() doesn't reach a bare `go`.
			defer safego.Recover("themebuild.runGeneration")
			s.runGeneration(context.WithoutCancel(ctx), c, next)
		}()
	case errors.Is(err, ErrGenerationInProgress):
		// Something else is already running for this chat — nothing more
		// to do here. That generation's own drain loop will dequeue this
		// row once it finishes (see runGeneration).
		emitter := newEventEmitter(ctx, s.repo, s.bus, genID, c.ID)
		emitter.emit(ctx, EventTypeQueued, map[string]any{
			"position": position, "prompt_preview": PromptPreview(in.Prompt),
		})
	default:
		return GenerateOutcome{}, fmt.Errorf("dequeue next generation: %w", err)
	}

	return GenerateOutcome{Chat: c, UserMessage: userMsg, QueuePosition: position, GenerationID: genID}, nil
}

// runGeneration drains chatID's queue one generation at a time, starting
// with g (the row DequeueNext already promoted to running to get here) and
// continuing until the queue is empty. Without the loop, only g itself
// would ever run — every prompt queued behind it would sit in the database
// forever with nothing left to dequeue it, since nothing else calls
// DequeueNext for a chat that already has something running.
//
// If g fails, the loop still continues to whatever's next: a failed
// generation earlier in the queue is not a reason to auto-cancel later,
// possibly-unrelated prompts the merchant queued behind it (see
// runOneQueuedGeneration/doGenerate — a failure is always recorded as a
// visible chat message, never silently swallowed).
func (s *Service) runGeneration(ctx context.Context, c chat.Chat, g Generation) {
	for {
		s.runOneQueuedGeneration(ctx, c, g)

		next, err := s.repo.DequeueNext(ctx, c.ID)
		if errors.Is(err, ErrNotFound) {
			return // queue drained
		}
		if err != nil {
			// Not ErrGenerationInProgress (this same loop is the only thing
			// that can be running for c.ID right now — EndGeneration inside
			// runOneQueuedGeneration always clears the running slot first)
			// — a genuine DB error. The reaper's periodic sweep will pick
			// this chat's queue back up as orphaned (see
			// ChatsWithOrphanedQueues) rather than retrying it in a tight
			// loop here.
			slog.Error("failed to dequeue next generation; the reaper will restart this chat's queue", "chat_id", c.ID, "error", err)
			return
		}
		g = next
	}
}

// runOneQueuedGeneration runs a single already-dequeued (status=running)
// generation to completion and records its outcome — one iteration of
// runGeneration's drain loop.
func (s *Service) runOneQueuedGeneration(ctx context.Context, c chat.Chat, g Generation) {
	emitter := newEventEmitter(ctx, s.repo, s.bus, g.ID, c.ID)
	emitter.emit(ctx, EventTypeDequeued, struct{}{})

	token, ok := s.tokens.take(g.ID)
	if !ok {
		// No token in memory for this generation — either this process
		// never served the request that enqueued it (a pod restart between
		// enqueue and dequeue) or it's being restarted by the reaper's own
		// orphaned-queue path (Part 4), which never had one to begin with.
		// Either way there's no bearer token left to forward to FlowPOS, so
		// this can't run — fail it with a message the merchant can act on
		// instead of either silently dropping it or calling FlowPOS
		// unauthenticated.
		//
		// Warn (not Error): a pod restart or a reaper-restarted queue
		// losing its token is an expected, already-handled condition, not
		// a bug — but it should still be visible. Before the heartbeat fix
		// (see the 20260813000002 migration), a slow-but-healthy
		// generation could cause the reaper to spuriously mark it stale
		// and orphan its queue, producing THIS exact message for every
		// prompt behind it despite nothing actually being wrong — logging
		// this is what makes a spike from that failure mode (or any other
		// unexpectedly frequent cause) visible instead of silent.
		slog.Warn("generation has no bearer token available; failing with session-expired", "chat_id", c.ID, "generation_id", g.ID)
		s.recordGenerationFailure(ctx, c, g.ID, errSessionExpired)
		endCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.repo.EndGeneration(endCtx, c.ID, errSessionExpired); err != nil {
			slog.Error("failed to record generation end", "chat_id", c.ID, "error", err)
		}
		return
	}

	in := GenerateInput{
		TenantID:      g.TenantID,
		Token:         token,
		ThemeSlug:     g.ThemeSlug,
		Prompt:        g.Prompt,
		ReferenceURL:  g.ReferenceURL,
		Mode:          g.Mode,
		UserMessageID: g.UserMessageID,
	}

	// Each drain-loop iteration gets its own fresh timeout — one shared
	// deadline across a queue of five prompts would starve the later ones
	// of their fair share of generateTimeout, or worse, kill them
	// mid-generation through no fault of their own. ctx itself is already
	// context.WithoutCancel of the original request (see Generate), so it
	// outlives any one HTTP call without being unbounded itself.
	workCtx, cancel := context.WithTimeout(ctx, generateTimeout())
	defer cancel()

	// A second, inner cancel layer over the timeout above, purely to
	// interrupt doGenerate promptly — NOT what tells its defer "the
	// merchant stopped this" (see cancelledByUser below for that): ctx can
	// arrive here already-canceled for entirely unrelated reasons (a
	// caller whose own request context died — see
	// TestDoGenerate_FailureEventStillWrittenOnAlreadyCanceledContext),
	// which is indistinguishable from userCancel() below by error type
	// alone (both are plain context.Canceled). cancelledByUser is the
	// explicit, unambiguous signal instead.
	workCtx, userCancel := context.WithCancel(workCtx)
	defer userCancel()

	// Set (before userCancel() is called, never after) by the listener
	// goroutine below the moment it recognizes a genuine cancel request
	// for THIS generation — read back both by doGenerate's own defer (to
	// decide whether to emit EventTypeCancelled instead of
	// EventTypeFailed) and by this function's own tail (to decide between
	// EndGenerationCancelled and EndGeneration). atomic because it's
	// written from the listener goroutine and read from this one.
	var cancelledByUser atomic.Bool

	// Listens for this specific generation's EventTypeCancelRequested (see
	// Service.CancelQueuedGeneration's running branch) and, on receipt,
	// cancels workCtx so doGenerate unwinds — same subscribe-by-chat-id
	// bus every connected client's stream uses, since a cancel request can
	// land on any replica, not necessarily the one actually running this
	// generation. Torn down via userCancelDone the moment this generation
	// ends for any other reason, so it never outlives the goroutine below.
	userCancelDone := make(chan struct{})
	defer close(userCancelDone)
	cancelEvents, cancelSub := s.bus.Subscribe(context.Background(), c.ID)
	defer cancelSub()

	// Closes the subscribe-after-cancel race: a cancel request can be
	// published (see CancelQueuedGeneration's running branch) in the gap
	// between DequeueNext marking this row running and the Subscribe call
	// just above — Publish only reaches subscribers already registered at
	// the moment it's sent, so that request would otherwise be silently
	// missed. RequestGenerationCancellation's durable write is what this
	// checks back; see its doc comment. The heartbeat ticker below
	// re-checks the same flag on every tick as a backstop for a dropped
	// live event too, so this one check only needs to close the narrow
	// startup race, not stand in for the live path generally.
	//
	// Bounded the same as every other ad hoc call in this function
	// (endCtx/hbCtx/emitCtx/commitCtx) — this one runs synchronously,
	// before the listener goroutine, heartbeat ticker, or doGenerate even
	// start, so an unbounded call here would stall this generation's
	// entire start on a slow/locked database instead of just this one
	// check.
	precheckCtx, precheckCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if requested, err := s.repo.IsCancellationRequested(precheckCtx, c.ID, g.ID); err == nil && requested {
		cancelledByUser.Store(true)
		userCancel()
	}
	precheckCancel()

	go func() {
		defer safego.Recover("themebuild.cancelListener")
		for {
			select {
			case <-userCancelDone:
				return
			case ev, ok := <-cancelEvents:
				if !ok {
					return
				}
				if ev.Type != EventTypeCancelRequested {
					continue
				}
				var payload struct {
					GenerationID string `json:"generation_id"`
				}
				if err := json.Unmarshal(ev.Payload, &payload); err != nil {
					continue
				}
				if payload.GenerationID == g.ID {
					cancelledByUser.Store(true)
					userCancel()
					return
				}
			}
		}
	}()

	// Heartbeat ticker — the second of two layers keeping generations
	// with a healthy but slow model call from being reaped mid-flight
	// (see the 20260813000002 migration and generationHeartbeatTimeout).
	// eventEmitter.emitLive (generation_events.go) already stamps the
	// heartbeat on every thinking delta, throttled the same
	// heartbeatThrottle interval — but ToolChoiceAny is forced on every
	// tool-loop iteration (see ai.Generate), so a turn that goes straight
	// to a tool call with no narration text produces no delta at all,
	// and nothing durable fires until the NEXT iteration boundary either
	// (tool_call/tool_result/checking/repairing — see emit's call sites).
	// A single slow call in between is exactly the gap the bug report
	// described. This ticker doesn't depend on what the model chooses to
	// emit: it only needs this goroutine to still be alive and running,
	// which is the actual thing the reaper cares about. It alone would
	// already fix the reaping bug; emitLive's heartbeat stays because it
	// reflects real progress rather than merely "the process hasn't
	// crashed", which is the more useful signal to see when reading the
	// generations table by hand — keeping it costs nothing once this
	// ticker exists as the robustness backstop.
	heartbeatTicker := time.NewTicker(heartbeatTickerInterval())
	defer heartbeatTicker.Stop()
	go func() {
		for {
			select {
			case <-workCtx.Done():
				return
			case <-heartbeatTicker.C:
				// Wrapped per-tick (not once for the whole goroutine): this
				// loop is meant to keep running for the generation's entire
				// duration, so one bad tick recovering shouldn't end
				// heartbeats for everything after it too.
				func() {
					defer safego.Recover("themebuild.heartbeatTicker")
					// Best-effort, matching UpdateGenerationHeartbeat's own
					// convention (see eventEmitter.emit): a fresh, short-lived
					// context rather than workCtx, since workCtx can already be
					// canceled by the time a tick lands right as the
					// generation finishes — a heartbeat write for a generation
					// about to be marked done/failed anyway is harmless to
					// lose, not worth erroring over.
					hbCtx, hbCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer hbCancel()
					if err := s.repo.UpdateGenerationHeartbeat(hbCtx, g.ID); err != nil {
						slog.Error("failed to update generation heartbeat (ticker)", "generation_id", g.ID, "error", err)
					}
					// Backstop for a cancel request whose live event was
					// dropped — EventTypeCancelRequested shares a bounded,
					// best-effort channel with high-frequency "thinking"
					// deltas (see eventBus's subscriberBufferSize) and can
					// be silently lost under load. Piggybacked on this
					// same tick rather than a second ticker: same cadence,
					// same already-open DB round trip's neighborhood, one
					// fewer goroutine.
					if requested, err := s.repo.IsCancellationRequested(hbCtx, c.ID, g.ID); err == nil && requested {
						cancelledByUser.Store(true)
						userCancel()
					}
				}()
			}
		}
	}()

	err := s.doGenerate(workCtx, in, c, g.ID, &cancelledByUser)

	// A deliberately fresh, short-lived context for this one bookkeeping
	// write: workCtx may already be expired (a generation that hit
	// generateTimeout, or that userCancel above ended early), and the
	// outcome still needs recording either way.
	endCtx, endCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer endCancel()
	// One unambiguous line per ended generation, logged before deciding
	// which of the two outcomes below to record — settles, from the logs
	// alone, whether a cut-short generation was an explicit stop request
	// (cancelled=true; cross-reference cancelOneRunning's own "cancel
	// requested" log for the same generation_id to see exactly when/how it
	// was asked to stop) or a genuine failure/timeout (cancelled=false;
	// error is the raw, unsanitized cause — never shown to the merchant,
	// see ai.SanitizeError, but exactly what's needed here to tell a real
	// timeout apart from, say, a context canceled for some other reason).
	// This is what earlier incidents lacked: without it, "it just stopped"
	// could only be diagnosed by inference from request timing.
	if err != nil {
		slog.Info("generation ended", "chat_id", c.ID, "generation_id", g.ID,
			"cancelled_by_user", cancelledByUser.Load(), "error", err.Error())
	}
	// err != nil is required here, not cancelledByUser.Load() alone: the
	// flag can still flip true after doGenerate has already committed a
	// real, successful result (see doGenerate's own defer, which applies
	// the identical guard for the same reason) — a nil err unambiguously
	// means the turn completed, and must never be relabeled "cancelled"
	// just because a cancel request happened to race its very end.
	if err != nil && cancelledByUser.Load() {
		if endErr := s.repo.EndGenerationCancelled(endCtx, c.ID); endErr != nil {
			slog.Error("failed to record generation end", "chat_id", c.ID, "error", endErr)
		}
	} else if endErr := s.repo.EndGeneration(endCtx, c.ID, err); endErr != nil {
		slog.Error("failed to record generation end", "chat_id", c.ID, "error", endErr)
	}
}

// errSessionExpired is what a queued generation fails with when it has no
// bearer token left to run with — see pendingTokens' doc comment. Sent to
// the merchant close to verbatim (never through ai.SanitizeError, which is
// for AI-provider failures and would mislabel this as one — see
// doGenerate's use of it and recordGenerationFailure).
var errSessionExpired = errors.New("your session expired before this prompt ran — send it again")

// recordGenerationFailure appends a merchant-visible failed assistant
// message and a "failed" event for a generation that never made it into
// doGenerate — currently only the "no auth token available" case (see
// runOneQueuedGeneration and reapOrphanedQueues in generation.go). A
// failure inside doGenerate already gets equivalent treatment from its own
// defer, which isn't reused here directly since it also closes over
// doGenerate's own stack (the emitter it already built, the summary
// variable, etc.) in a way that doesn't factor out cleanly.
func (s *Service) recordGenerationFailure(ctx context.Context, c chat.Chat, genID string, err error) {
	slog.Error("generation failed before it could start", "chat_id", c.ID, "tenant_id", c.TenantID, "error", err)
	emitter := newEventEmitter(ctx, s.repo, s.bus, genID, c.ID)
	emitter.emit(ctx, EventTypeFailed, map[string]string{"message": err.Error()})
	if _, recErr := s.chats.RecordAssistantMessage(ctx, c, err.Error(), chat.MessageStatusFailed, 0, 0, chat.ApplyStatusNotApplicable); recErr != nil {
		slog.Error("failed to record failed-generation chat message", "chat_id", c.ID, "error", recErr)
	}
}

// fetchReferenceURL is s.links.Fetch, plus fetching the page's stylesheets
// (s.links.FetchStylesheets) and building a compact structured digest from
// the two (urlfetch.BuildDigest) — see BuildDigest's own doc comment for
// why a digest, not raw markup, is what a link-fetched reference sends to
// the model. A short-TTL cache sits in front of the whole thing, holding
// that FINISHED digest text (see referenceURLCache's own doc comment for
// why post-digest, not the raw fetch, is what's cached) — a merchant
// iterating on the same reference across several turns in one session
// shouldn't pay for the HTML fetch, the stylesheet fetches, AND building
// the digest again, on every single one. Falls back to an uncached call
// when s.linkCache is nil (struct-literal tests, same nil-guard convention
// as s.links itself).
//
// Unlike the upload path (Generate's HTML-attachment handling, which
// still runs an uploaded file's raw content through
// SanitizeHTMLAttachment unchanged — that file IS what the model reads
// verbatim, so stripping script/SVG/base64 out of it still matters),
// SanitizeHTMLAttachment plays no role here any more: BuildDigest performs
// STRUCTURAL EXTRACTION, walking the document's own tokens to pull out
// headings/copy/design tokens rather than stripping a few dangerous
// patterns out of markup that's otherwise sent through as-is. A link's raw
// HTML never reaches the model at all any more, only what BuildDigest
// extracted from it, so there is nothing left for the sanitizer to
// usefully remove.
func (s *Service) fetchReferenceURL(ctx context.Context, tenantID uint64, url string, maxBytes int64) (content string, truncated, empty bool, title string, styleCount int, err error) {
	if s.linkCache != nil {
		if cached, wasTruncated, ok := s.linkCache.get(tenantID, url); ok {
			// Latency: a hit returns the already-built digest directly and
			// skips fetching stylesheets and building a fresh digest
			// entirely — a cache hit is strictly faster than a miss, not
			// just smaller to store. Title and styleCount aren't part of
			// what the cache stores (a cache hit is never Empty by
			// construction — see the set call below — so there's nothing
			// for a caller to route to the empty-after-fetch path either):
			// the merchant-facing "fetched N stylesheets" narration this
			// turn just reflects nothing new having actually been fetched,
			// which is accurate.
			return cached, wasTruncated, false, "", 0, nil
		}
	}
	result, err := s.links.Fetch(ctx, url, maxBytes)
	if err != nil {
		return "", false, false, "", 0, err
	}
	css, styleCount := s.links.FetchStylesheets(ctx, result.HTML, result.FinalURL)
	digest := urlfetch.BuildDigest(result.FinalURL, result.HTML, css)
	content = digest.Text
	// Either cause counts: the raw HTML fetch itself may have been cut off
	// (result.Truncated), OR the page fetched in full but extraction alone
	// still produced more than digestHardCapBytes once combined
	// (digest.Truncated) — see Digest.Truncated's own doc comment. Either
	// way the merchant-facing "this copy was cut short" note needs to fire.
	truncated = result.Truncated || digest.Truncated
	// PostStripMaxBytes is now a BACKSTOP, not the normal path:
	// digestHardCapBytes (16KB) is already comfortably under
	// PostStripMaxBytes (300KB), so a real digest should never actually
	// reach this branch — kept only so a future change to either constant
	// can't silently reintroduce an oversized reference-link attachment.
	postStripMax := attachmentLimits[chat.AttachmentKindHTML].PostStripMaxBytes
	if int64(len(content)) > postStripMax {
		content = urlfetch.TruncateAtTagBoundary(content, postStripMax)
		truncated = true
	}
	// digest.Empty (no headings, no landmarks, effectively no copy —
	// measured on the digest's own EXTRACTED text, not on raw HTML byte
	// length; see Digest.Empty's own doc comment) is the client-rendered-
	// shell case — not a successful fetch+digest in the sense
	// referenceURLCache.set requires, even though no error occurred:
	// caching it would hold an empty shell for the full TTL, contradicting
	// the cache's own "successes only" contract. Unlike a network failure,
	// this result is deterministic for a given page — skipping the cache
	// here just costs a repeat fetch next turn, not a repeat of whatever
	// actually went wrong.
	if s.linkCache != nil && !digest.Empty {
		s.linkCache.set(tenantID, url, content, truncated)
	}
	return content, truncated, digest.Empty, digest.Title, styleCount, nil
}

// truncatedLengthTolerance bounds how far under PostStripMaxBytes a
// persisted attachment's stored length can be and still be inferred as
// truncated (see looksTruncatedByStoredLength) — chat_message_attachments
// has no truncated column of its own, so a later turn's carry-forward has
// only the stored length to go on. An exact-cap comparison misses the case
// where the cut landed mid-tag: TruncateAtTagBoundary backs up to the last
// "<" (and, on top of that, may trim a few more bytes to avoid splitting a
// rune — see trimIncompleteTrailingRune in urlfetch), so the stored length
// can land noticeably short of PostStripMaxBytes even though the fetch was
// genuinely truncated. 4096 is comfortably larger than any single HTML tag
// plus a trailing rune, while still far below any plausible real page that
// just happens to end within a few KB of the cap by coincidence. This is a
// heuristic over a stored length, not a persisted fact, so it's allowed to
// be wrong in either direction — a false positive only costs an
// unnecessary "this was cut short" note in the prompt, while a false
// negative costs that note on a turn that actually needed it. The
// tolerance is sized to favor the former, cheaper mistake.
const truncatedLengthTolerance = 4096

// looksTruncatedByStoredLength reports whether contentLength is close
// enough to PostStripMaxBytes to infer the stored HTML attachment was
// truncated on write — see truncatedLengthTolerance's own doc comment for
// why this is a range check, not an exact comparison.
func looksTruncatedByStoredLength(contentLength int64) bool {
	postStripMax := attachmentLimits[chat.AttachmentKindHTML].PostStripMaxBytes
	return contentLength >= postStripMax-truncatedLengthTolerance
}

// doGenerate is the part of generation that used to be Generate's entire
// body before it became async: ask Claude for the resulting file changes
// and stage them into the chat's draft overlay — see this package's own
// doc comment for the draft/apply split; writing to the real theme is a
// separate, explicit Service.ApplyDraft step, not something this does.
// cancelledByUser is nil from tests that drive doGenerate directly with no
// cancel machinery of their own (see
// TestDoGenerate_FailureEventStillWrittenOnAlreadyCanceledContext) — only
// runOneQueuedGeneration ever passes a real one.
func (s *Service) doGenerate(ctx context.Context, in GenerateInput, c chat.Chat, genID string, cancelledByUser *atomic.Bool) (retErr error) {
	emitter := newEventEmitter(ctx, s.repo, s.bus, genID, c.ID)
	emitter.emit(ctx, EventTypeStarted, struct{}{})

	// summary is declared here (not with := at its point of use below) so
	// this defer's closure captures the same variable and sees its final
	// value — a "done" event needs the actual summary, not a placeholder.
	var summary string
	// doGenerateStart/hasChanges back the end-to-end wall-clock log below —
	// this is the number the merchant actually experiences; everything
	// internal/ai logs explains it. hasChanges is declared here (rather
	// than with := at its normal point of use further down) purely so this
	// defer's closure can see its final value the same way summary above
	// does; it's assigned, never redeclared, below.
	doGenerateStart := time.Now()
	var hasChanges bool
	defer func() {
		slog.Info("ai: generation wall-clock", "chat_id", c.ID, "mode", in.Mode,
			"elapsed_ms", time.Since(doGenerateStart).Milliseconds(), "has_changes", hasChanges)
	}()
	defer func() {
		// A deliberately fresh, short-lived context for this defer's own
		// writes — mirrors runGeneration's endCtx pattern. ctx itself may
		// already be expired here (the common failure case this defer
		// exists for: a generation that hit generateTimeout, or whose
		// caller's context was canceled) — emitting on a dead ctx makes
		// AppendGenerationEvent (and RecordAssistantMessage below) silently
		// no-op, which is exactly the bug this fixes: a timed-out
		// generation with a dead ctx would emit nothing and leave the
		// WebSocket (and the chat) with no record of why it failed.
		emitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// retErr != nil is required alongside cancelledByUser, not
		// cancelledByUser alone: the listener/backstop can flip it true at
		// any point relative to this function's own progress, including
		// after RecordAssistantMessage + persistFileRecords below have
		// already committed a real, successful result — commitCtx (see
		// below) makes that commit itself immune to cancellation once
		// reached, so retErr == nil there unambiguously means the turn
		// really did complete and must never be relabeled "cancelled"
		// just because a request happened to race its very end.
		//
		// Deliberately NOT ctx.Err()-based either: ctx can arrive here
		// already canceled for reasons that have nothing to do with a
		// merchant cancel request (a caller whose own request context died
		// — see TestDoGenerate_FailureEventStillWrittenOnAlreadyCanceledContext),
		// which is indistinguishable from a real cancel by error type
		// alone. cancelledByUser is the explicit, unambiguous signal.
		if retErr != nil && cancelledByUser != nil && cancelledByUser.Load() {
			// A merchant-requested stop (see
			// Service.CancelQueuedGeneration's running branch and
			// runOneQueuedGeneration's cancelledByUser), not a failure — no
			// failed message gets recorded, mirroring how cancelling a
			// still-queued prompt already leaves no chat message behind
			// either.
			//
			// retErr is still logged if it ISN'T the expected
			// context.Canceled this cancellation itself produces:
			// cancelledByUser being true only means a cancel request was
			// observed at some point, not that it's what caused retErr —
			// a genuinely unrelated failure (a DB error, an AI-provider
			// error) can coincidentally race a cancel request landing at
			// the same moment. Silently treating every such coincidence as
			// "just a cancellation" would erase the one place doGenerate's
			// real failures are diagnosable server-side (see the retErr !=
			// nil branch below, which logs unconditionally).
			if !errors.Is(retErr, context.Canceled) {
				slog.Error("generation failed (raced a concurrent cancel request)", "chat_id", c.ID, "tenant_id", in.TenantID, "error", retErr)
			}
			emitter.emit(emitCtx, EventTypeCancelled, map[string]string{"generation_id": genID})
			return
		}

		if retErr != nil {
			// Never surface retErr.Error() directly — it can contain the
			// backing AI provider's name/URL/request ID (see
			// ai.SanitizeError's doc comment). Log the raw error here, the one
			// place doGenerate's failure is fully known, so a generic
			// "something went wrong" shown to the merchant is still
			// diagnosable server-side.
			slog.Error("generation failed", "chat_id", c.ID, "tenant_id", in.TenantID, "error", retErr)
			message := ai.SanitizeError(retErr)
			if isUnauthorizedErr(retErr) {
				// A queued generation's token can go stale before its turn
				// comes up (see "Auth for queued work" / pendingTokens) —
				// the model was never even called here, so
				// ai.SanitizeError's generic "AI agent" framing would be
				// actively misleading. Give the merchant the one thing that
				// actually explains it and tells them what to do.
				message = errSessionExpired.Error()
			}
			emitter.emit(emitCtx, EventTypeFailed, map[string]string{"message": message})
			if _, err := s.chats.RecordAssistantMessage(emitCtx, c, message, chat.MessageStatusFailed, 0, 0, chat.ApplyStatusNotApplicable); err != nil {
				slog.Error("failed to record failed-generation chat message", "chat_id", c.ID, "error", err)
			}
		} else {
			emitter.emit(emitCtx, EventTypeDone, map[string]string{"summary": summary})
		}
	}()

	storeAuth := themefs.RequestAuth{Token: in.Token, TenantID: in.TenantID}

	// The draft overlay this whole feature exists for: every prior turn's
	// still-'pending' file content, read first before falling through to
	// the real (last-applied) theme — see themefs.OverlayStore. Built once
	// per generation call and threaded through every read the rest of this
	// function does; store (not s.store) is what buildThemeContext,
	// buildToolExecutor's three tools, buildSnapshot, and buildWritePlan
	// all read from, so a second/third prompt in this chat always sees
	// what earlier turns in the SAME draft already changed, never the
	// stale saved theme. See doGenerate's package-level doc comment.
	draft, err := s.repo.DraftFiles(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("load draft overlay: %w", err)
	}
	store := themefs.NewOverlayStore(s.store, draft)

	priorMessages, err := s.chats.ListMessages(ctx, in.TenantID, c.ID)
	if err != nil {
		return fmt.Errorf("load chat history: %w", err)
	}

	// in arrives here rebuilt fresh from the generations row (see
	// runOneQueuedGeneration) — it never carries an image straight from
	// Generate's own local scope, since a dequeue can happen well after
	// that scope returns. Re-resolve it from the just-loaded history
	// instead: in.UserMessageID (carried through Generation.UserMessageID)
	// points at the exact chat_messages row Generate wrote it to. in is a
	// value parameter, so this reassignment is local to this call only —
	// never leaks back to the caller.
	//
	// priorMessages carries attachment METADATA only (see
	// chat.MessageAttachment's own doc comment) — the len(m.Attachments) >
	// 0 check below is what keeps the overwhelmingly common
	// zero-attachment turn from costing a second query: GetAttachmentsContent
	// (the one call that actually pulls bytes out of MySQL) only runs when
	// this turn's own message is known, from metadata already in hand, to
	// have something to fetch.
	if in.UserMessageID != nil {
		for _, m := range priorMessages {
			if m.ID != *in.UserMessageID {
				continue
			}
			if len(m.Attachments) == 0 {
				break
			}
			full, attErr := s.chats.GetAttachmentsContent(ctx, *in.UserMessageID)
			if attErr != nil {
				return fmt.Errorf("load attachment content: %w", attErr)
			}
			for _, a := range full {
				switch a.Kind {
				case chat.AttachmentKindImage:
					in.Images = append(in.Images, chat.MessageImage{
						Base64:    base64.StdEncoding.EncodeToString(a.Content),
						MediaType: a.MediaType,
					})
				case chat.AttachmentKindHTML:
					filename := a.Filename
					content := string(a.Content)
					in.HTMLAttachmentFilename = &filename
					in.HTMLAttachmentContent = &content
					in.HTMLAttachmentIsExternalLink = looksLikeFetchedLink(filename)
					// See looksTruncatedByStoredLength's own doc comment for
					// why this is a tolerance range, not an exact-cap check.
					in.HTMLAttachmentTruncated = looksTruncatedByStoredLength(int64(len(content)))
				default:
					// Repository already filters unknown kinds before they
					// get here — this is defense in depth, not the primary
					// enforcement (see Repository.GetAttachmentsContent).
					slog.Warn("doGenerate: unknown attachment kind, skipping", "kind", a.Kind, "attachment_id", a.ID)
				}
			}
			break
		}
	}

	// Reference-URL fetch: Generate only detected the URL and validated its
	// shape (see its own doc comment on why the actual fetch is deferred to
	// here) — this is where the network call happens, now that a
	// generation is genuinely running in the background and slow work is
	// safe. Runs before the carry-forward fallback below so a reference
	// on THIS turn always wins over an earlier turn's, the same precedence
	// Generate already applies for an uploaded file vs. a URL in the
	// prompt.
	if in.HTMLAttachmentContent == nil && in.ReferenceURL != "" && s.links != nil {
		emitter.emit(ctx, EventTypeFetchingLink, map[string]string{"url": in.ReferenceURL})
		htmlLimit := attachmentLimits[chat.AttachmentKindHTML]
		// Fetching the HTML, fetching its stylesheets, and building the
		// digest all happen entirely inside fetchReferenceURL now — see
		// its own doc comment for why: keeping it in exactly one place
		// also lets a cache hit skip all three, not just re-fetching.
		digestContent, truncated, digestEmpty, title, styleCount, ferr := s.fetchReferenceURL(ctx, in.TenantID, in.ReferenceURL, htmlLimit.MaxBytes)
		// A page whose real content lives entirely in client-rendered
		// <script> blocks (a React/Vue SPA whose server-sent HTML is just
		// an empty mount point) digests to nothing usable — no headings,
		// no landmarks, no real copy (see Digest.Empty's own doc comment).
		// Treated as a failure, not a success with an empty attachment —
		// see ReferenceURLEmptyAfterSanitize's own doc comment.
		emptyAfterSanitize := ferr == nil && digestEmpty
		// Must NOT fail the turn either way — the merchant asked a real
		// question and deserves an answer about everything except the
		// page (see ReferenceURLFetchFailed's own doc comment).
		// promptWithHTMLAttachment tells the model the fetch failed
		// instead of silently proceeding as if no link had ever been
		// mentioned.
		switch {
		case ferr != nil:
			slog.Warn("reference URL fetch failed", "chat_id", c.ID, "url", in.ReferenceURL, "error", ferr)
			in.ReferenceURLFetchFailed = true
			in.ReferenceURLBlocked = errors.Is(ferr, urlfetch.ErrBlocked)
		case emptyAfterSanitize:
			slog.Warn("reference URL digest was empty (likely client-rendered)", "chat_id", c.ID, "url", in.ReferenceURL)
			in.ReferenceURLFetchFailed = true
			in.ReferenceURLEmptyAfterSanitize = true
		default:
			// Narration that something real happened — see
			// EventTypeFetchedLink's own doc comment for why this only
			// fires on this success path, not on a failure or an empty
			// digest (both already have their own explanation via the
			// turn's eventual reply).
			emitter.emit(ctx, EventTypeFetchedLink, map[string]any{"title": title, "stylesheet_count": styleCount})

			filename := in.ReferenceURL
			in.HTMLAttachmentFilename = &filename
			in.HTMLAttachmentContent = &digestContent
			in.HTMLAttachmentIsExternalLink = true
			in.HTMLAttachmentTruncated = truncated

			// Persisted so findCarryForwardSourceMessageID still works on a
			// later turn — that lookup reads chat_message_attachments, and
			// nothing else writes there for a link now that RecordUserMessage
			// no longer sees the fetched content (only Generate's raw
			// detection, before any network call). filename is capped to 255
			// (the column's width — urlfetch.maxURLLen is 2048, so a long URL
			// would otherwise blow up the insert under strict SQL mode) while
			// keeping the "https://" prefix intact so looksLikeFetchedLink
			// still matches on read-back; the full URL still goes to the
			// model via HTMLAttachmentFilename above, only the persisted copy
			// is shortened. Best-effort: logged and swallowed on failure — a
			// lost persist only costs carry-forward on a later turn, it must
			// not fail a generation that already has the content in hand.
			//
			// What's stored here is the DIGEST now, not raw HTML — a real
			// change to chat_message_attachments.content's meaning for a
			// link-kind attachment going forward. An OLD row from before
			// this phase still holds raw sanitized markup; that reads back
			// fine on a later carry-forward turn (see doGenerate's
			// carry-forward block below) — it's still real page content the
			// model can use, just not in digest form — so no backfill or
			// migration is needed for it.
			if in.UserMessageID != nil {
				storedFilename := filename
				if len(storedFilename) > 255 {
					storedFilename = storedFilename[:255]
				}
				if attErr := s.chats.AttachHTMLToMessage(ctx, *in.UserMessageID, in.TenantID, storedFilename, digestContent); attErr != nil {
					slog.Error("failed to persist fetched reference URL as an attachment", "chat_id", c.ID, "error", attErr)
				}
			}
		}
	}

	// Carry-forward fallback: this turn attached no HTML reference of its
	// own — look back for the most recent earlier turn in this chat that
	// did, still inside the window actually replayed to the model (see
	// findCarryForwardSourceMessageID). Without this, "here's a link"
	// followed later by "build it like that" runs the actual build with no
	// page content at all — the model designs from its own earlier summary
	// of the page, not the page itself. currentID is empty (never matches a
	// real message.ID) when in.UserMessageID is nil, which only happens in
	// tests that drive doGenerate directly — harmless: nothing to exclude
	// from the scan in that case either.
	if in.HTMLAttachmentContent == nil {
		currentID := ""
		if in.UserMessageID != nil {
			currentID = *in.UserMessageID
		}
		if sourceID, ok := findCarryForwardSourceMessageID(priorMessages, currentID); ok {
			full, attErr := s.chats.GetAttachmentsContent(ctx, sourceID)
			if attErr != nil {
				return fmt.Errorf("load carried-forward attachment content: %w", attErr)
			}
			for _, a := range full {
				if a.Kind != chat.AttachmentKindHTML {
					continue
				}
				filename := a.Filename
				content := string(a.Content)
				in.HTMLAttachmentFilename = &filename
				in.HTMLAttachmentContent = &content
				in.HTMLAttachmentIsExternalLink = looksLikeFetchedLink(filename)
				// See the identical check in the current-turn attachment
				// block above — same heuristic, same reason.
				in.HTMLAttachmentTruncated = looksTruncatedByStoredLength(int64(len(content)))
				in.HTMLAttachmentCarriedForward = true
				slog.Info("carried forward an earlier turn's HTML reference", "chat_id", c.ID,
					"source_message_id", sourceID, "is_link", in.HTMLAttachmentIsExternalLink)
				break
			}
		}
	}

	tc, err := s.buildThemeContext(ctx, store, storeAuth, in.ThemeSlug)
	if err != nil {
		return fmt.Errorf("load theme context: %w", err)
	}
	tc.GenerationMode = in.Mode

	toolExec := s.buildToolExecutor(store, storeAuth)
	readFile := s.buildFileReader(store, storeAuth)

	turns := s.summarizeOldTurnsCached(ctx, c.ID, toTurns(priorMessages))
	result, turns, err := s.generateValidProposal(ctx, tc, turns, in.Prompt, toolExec, readFile, emitter, in)
	if err != nil {
		return err
	}

	var warnings []themecheck.Finding
	if proposalHasChanges(result) {
		// Emitted for the model's first accepted propose_changes call, not
		// whatever checkAndRepair's retries eventually settle on below — by
		// the time a repair retry replaces result, the merchant watching
		// the step list has already seen "Writing N files…" once for this
		// turn, which is the narration point that matters (a repair retry
		// changing the exact count isn't worth a second, confusing
		// "Writing M files…" for the same turn).
		emitter.emit(ctx, EventTypeProposing, map[string]int{"file_count": len(result.Files)})

		snap, err := s.buildSnapshot(ctx, store, storeAuth, result)
		if err != nil {
			return fmt.Errorf("build theme snapshot: %w", err)
		}
		result, warnings, err = s.checkAndRepair(ctx, in, c.ID, tc, turns, result, snap, toolExec, readFile, emitter)
		if err != nil {
			return err
		}
	}

	hasChanges = proposalHasChanges(result)

	var staged []writtenFile
	if hasChanges {
		// Still locked, even though nothing is written to FlowPOS here
		// anymore — buildWritePlan still READS the layout files and the
		// theme's current file list, and two concurrent generations for
		// the same theme staging at once could otherwise compute their
		// layout splices against an inconsistent view of each other's
		// in-flight (but not yet persisted-as-pending) draft. Scoped
		// tightly to just this section per its existing convention (see
		// themeLocks' own doc comment) — the Claude call above can take
		// minutes, and a second tab's turn shouldn't queue behind that.
		unlock, err := s.themeLocks.Lock(ctx, themeLockKey(in.TenantID, in.ThemeSlug))
		if err != nil {
			return fmt.Errorf("stage theme changes: %w", err)
		}
		defer unlock()

		// Computed entirely in memory first, nothing staged yet: a
		// failure here (e.g. a duplicate page slug) leaves the draft
		// completely untouched, rather than a validation error arriving
		// after some files already landed in chat_generated_files with
		// nothing recording that they did.
		plan, err := s.buildWritePlan(ctx, store, storeAuth, result)
		if err != nil {
			return fmt.Errorf("stage theme changes: %w", err)
		}
		emitter.emit(ctx, EventTypeStaged, map[string]any{"paths": plan.paths()})

		// No commitWritePlan call — this is the entire point of the
		// draft/apply split (see themebuild's package doc comment):
		// nothing reaches FlowPOS here. planToStaged turns the plan into
		// the same writtenFile shape persistFileRecords already knows how
		// to audit, including the layout splices (see GeneratedFileKind)
		// that used to be silently un-audited when writes were immediate.
		staged = planToStaged(plan)
	}

	applyStatus := chat.ApplyStatusNotApplicable
	if hasChanges {
		applyStatus = chat.ApplyStatusPending
	}

	// The schema requires "summary" as a key but not a non-empty one, so an
	// empty string is a valid (if unhelpful) reply the model can return. A
	// "completed" turn with empty content isn't just a bland reply, though —
	// it's a landmine: internal/ai applies a prompt-cache breakpoint to the
	// last history turn on every subsequent call, and Anthropic rejects
	// cache_control on an empty text block outright (400), which would take
	// down every future message in this chat, not just this one. Never
	// persist that state.
	summary = result.Summary
	if summary == "" {
		summary = "Done."
	}
	summary = appendWarningsNote(summary, warnings)

	// A cancel request landing in this exact window — after the model's
	// output has already been decided and is only being committed — must
	// never be allowed to leave a "completed" assistant message
	// referencing files that were only partially written, or vice versa.
	// commitCtx is deliberately detached from ctx (rooted at
	// context.Background(), not derived from it, with its own bounded
	// timeout — the same pattern as endCtx/emitCtx elsewhere in this
	// file) so a cancellation can only ever stop this turn BEFORE this
	// point, never in the middle of recording its result.
	commitCtx, commitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer commitCancel()

	assistantMsg, err := s.chats.RecordAssistantMessage(commitCtx, c, summary, chat.MessageStatusCompleted, result.InputTokens, result.OutputTokens, applyStatus)
	if err != nil {
		return fmt.Errorf("record assistant message: %w", err)
	}

	if _, err := s.persistFileRecords(commitCtx, c, assistantMsg.ID, staged); err != nil {
		return fmt.Errorf("persist generated-file audit rows: %w", err)
	}

	return nil
}

// GenerationStatus reports whether chatID currently has a background
// Generate call running, and the error from the most recently finished one
// if it failed (cleared as soon as the next generation starts) — backed by
// the generations table (phase 3a), so this survives a pod restart and is
// correct with more than one replica, unlike the in-memory tracker it
// replaced. A chat with no generation row yet (never sent a first message)
// reports not-generating, no error — a normal state, not an error itself.
func (s *Service) GenerationStatus(ctx context.Context, chatID string) (generating bool, errMsg string) {
	g, err := s.repo.GetGeneration(ctx, chatID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, ""
		}
		slog.Error("failed to load generation status", "chat_id", chatID, "error", err)
		return false, ""
	}
	if g.Status != GenerationStatusRunning && g.Error != nil {
		return false, *g.Error
	}
	return g.Status == GenerationStatusRunning, ""
}

// manifestGenerator is the *themefs.Store-only capability buildThemeContext
// needs beyond themefs.ThemeStore (see ThemeStore's own doc comment on why
// GetOrGenerateManifest isn't part of it). store here is whatever this
// generation call is actually reading from (the draft overlay, mid-
// generation) — its manifest is always generated from s.store (the real,
// non-overlay store, see Service.store's doc comment), never the draft:
// making the component-signature manifest draft-aware isn't required by
// this feature and manifest caching is keyed to the real theme's own
// fingerprint (see manifest.go), which a draft has no independent notion of.
type manifestGenerator interface {
	GetOrGenerateManifest(ctx context.Context, auth themefs.RequestAuth) (themefs.Manifest, error)
}

// rawAssetReader is the *themefs.Store-only capability ReadThemeAssetBytes
// needs, same reasoning as manifestGenerator above: ThemeStore's ReadFile
// returns a string, which silently corrupts binary content (theme images/
// fonts) — see themefs.Store.ReadFileBytes's own doc comment.
type rawAssetReader interface {
	ReadFileBytes(ctx context.Context, auth themefs.RequestAuth, relPath string) ([]byte, error)
}

// storeSettingsFetcher is the *themefs.Store-only capability
// FetchStoreSettings needs, same reasoning as rawAssetReader above: store
// settings aren't a theme file, so they're not part of ThemeStore.
type storeSettingsFetcher interface {
	FetchStoreSettings(ctx context.Context, auth themefs.RequestAuth) (themefs.StoreSettings, error)
}

// productsFetcher is the *themefs.Store-only capability FetchPreviewProducts
// needs, same reasoning as storeSettingsFetcher above: a real products list
// isn't a theme file, so it's not part of ThemeStore.
type productsFetcher interface {
	FetchProducts(ctx context.Context, auth themefs.RequestAuth, limit int) (themefs.ProductsPage, error)
}

// FetchPreviewProducts fetches the tenant's real published, active products
// (first page, capped at limit) for PreviewHandler's buildPreviewContext, so
// the AI-chat preview's shop/product-detail pages show real product data
// instead of FixtureProducts' canned "Sample Product" — see
// themefs.Store.FetchProducts' own doc comment for the endpoint and filters
// used.
func (s *Service) FetchPreviewProducts(ctx context.Context, storeAuth themefs.RequestAuth, limit int) (themefs.ProductsPage, error) {
	fetcher, ok := s.store.(productsFetcher)
	if !ok {
		return themefs.ProductsPage{}, fmt.Errorf("theme store does not support products fetch")
	}
	return fetcher.FetchProducts(ctx, storeAuth, limit)
}

// FetchStoreSettings fetches the tenant's real store settings (currently
// just its name) for PreviewHandler's buildPreviewContext, so the AI-chat
// preview's header shows the merchant's actual store name instead of
// FixtureContext's canned "Sample Store".
func (s *Service) FetchStoreSettings(ctx context.Context, storeAuth themefs.RequestAuth) (themefs.StoreSettings, error) {
	fetcher, ok := s.store.(storeSettingsFetcher)
	if !ok {
		return themefs.StoreSettings{}, fmt.Errorf("theme store does not support store settings fetch")
	}
	return fetcher.FetchStoreSettings(ctx, storeAuth)
}

// FetchThemeMenu reads the tenant's real defaults.json and returns its
// menu object (`items[]` — see theme_engine_spec.md §6) for
// PreviewHandler's buildPreviewContext, so the AI-chat preview's nav shows
// every link the merchant has actually configured instead of
// FixtureContext's static 3-link fallback. Unlike FetchStoreSettings, menu
// data lives in a theme file, so it's read straight off ThemeStore —
// no *themefs.Store-only capability interface needed.
func (s *Service) FetchThemeMenu(ctx context.Context, storeAuth themefs.RequestAuth) (map[string]any, error) {
	raw, err := s.store.ReadFile(ctx, storeAuth, pathDefaultsJSON)
	if err != nil {
		return nil, fmt.Errorf("read defaults.json: %w", err)
	}
	var parsed struct {
		Menu map[string]any `json:"menu"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse defaults.json: %w", err)
	}
	items, _ := parsed.Menu["items"].([]any)
	if len(items) == 0 {
		return nil, fmt.Errorf("defaults.json has no menu items")
	}
	return parsed.Menu, nil
}

// ReadThemeAssetBytes fetches one theme file's raw bytes, authenticated as
// storeAuth — backs AssetHandler, which lets the frontend's client-side
// LiquidJS preview (a sandboxed iframe with no real origin — see
// tenant-dashboard's usePreviewDoc.ts) resolve an <img src="/theme-assets/
// ...restrictive-path"> reference into actual image bytes instead of a
// broken relative path. Always reads the real theme (s.store, not a draft
// overlay): images aren't something a generation turn's proposal ever
// writes, so there's no draft-vs-real distinction to make here.
func (s *Service) ReadThemeAssetBytes(ctx context.Context, storeAuth themefs.RequestAuth, relPath string) ([]byte, error) {
	reader, ok := s.store.(rawAssetReader)
	if !ok {
		return nil, fmt.Errorf("theme store does not support raw asset reads")
	}
	return reader.ReadFileBytes(ctx, storeAuth, relPath)
}

func (s *Service) buildThemeContext(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, themeSlug string) (ai.ThemeContext, error) {
	pagesJSON, err := store.ReadFile(ctx, storeAuth, pathPagesJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	defaultsJSON, err := store.ReadFile(ctx, storeAuth, pathDefaultsJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	var manifest themefs.Manifest
	if mg, ok := s.store.(manifestGenerator); ok {
		manifest, err = mg.GetOrGenerateManifest(ctx, storeAuth)
		if err != nil {
			return ai.ThemeContext{}, fmt.Errorf("build manifest: %w", err)
		}
	}
	return ai.ThemeContext{
		ThemeSlug:    themeSlug,
		PagesJSON:    pagesJSON,
		DefaultsJSON: defaultsJSON,
		FileTree:     tree,
		Manifest:     &manifest,
	}, nil
}

// buildSnapshot fetches the current theme's full file-path listing (every
// path that exists, for rule 4's render-target-exists check — see
// themecheck.Snapshot.Paths) plus real content for the handful of files
// themecheck actually reads (pages.json, defaults.json, the two layout
// files) — plus, for every file result proposes to "update", that file's
// real current content too (see themecheck.checkPlaceholderBody's
// content-shrink check, which needs a real "before" to compare the
// proposal's "after" against — a page's prior content was never loaded
// into the snapshot before this, so that check had nothing to compare
// with). That same per-file "before" content is also what
// themecheck.DowngradePreExistingFindings uses as its baseline (see
// checkAndRepair) to tell a violation the merchant's theme already had from
// one this proposal just introduced — sourced from store, the same overlay
// store the model's own read_theme_file tool reads through, so it reflects
// what the model actually saw, staged draft changes from earlier turns
// included. Called once per doGenerate call, before the check-and-repair
// loop: nothing is written to the theme until after that loop accepts a
// proposal, so the same snapshot — and the same pre-generation baseline —
// is valid across every retry within one call, never a prior failed
// attempt's own output (checkAndRepair keeps re-using this same snapshot;
// only fresh update paths that first appear on a retry would miss a
// "before" here, same as before this change for any path).
func (s *Service) buildSnapshot(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, result *ai.Result) (themecheck.Snapshot, error) {
	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		return themecheck.Snapshot{}, fmt.Errorf("list theme files: %w", err)
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	files := make(map[string]string, 4)
	for _, path := range []string{pathPagesJSON, pathDefaultsJSON, pathLayoutStart, pathLayoutEnd} {
		content, err := store.ReadFile(ctx, storeAuth, path)
		if err != nil {
			return themecheck.Snapshot{}, fmt.Errorf("read %s: %w", path, err)
		}
		files[path] = content
	}
	for _, f := range result.Files {
		if f.Action != "update" {
			continue
		}
		if _, ok := files[f.Path]; ok {
			continue
		}
		content, err := store.ReadFile(ctx, storeAuth, f.Path)
		if err != nil {
			// Fails open, unlike the four required files above: this fetch
			// only backs the placeholder-body "before" compare and the
			// pre-existing-violation baseline, both optional refinements —
			// missing it just means that one file falls back to today's
			// stricter behavior (no grandfathering, no shrink check) rather
			// than failing the whole generation over a network hiccup to
			// FlowPOS.
			slog.Warn("failed to fetch baseline content for proposed file; treating it as having no baseline",
				"path", f.Path, "error", err)
			continue
		}
		files[f.Path] = content
	}

	return themecheck.Snapshot{Files: files, Paths: paths}, nil
}

// flattenFileTree walks a theme's file tree (see themefs.Store.ListFiles),
// recording every FILE path (not directories) into paths.
func flattenFileTree(entries []themefs.FileTreeEntry, paths map[string]bool) {
	for _, e := range entries {
		if e.Type == "file" {
			paths[e.Path] = true
		}
		if len(e.Children) > 0 {
			flattenFileTree(e.Children, paths)
		}
	}
}

// toTurns replays a chat's history as message turns for the model. Anthropic
// rejects an empty text content block outright ("text content blocks must
// be non-empty") — not just for the cache_control breakpoint, for any
// message anywhere in the request — so an empty turn is skipped rather than
// replayed, regardless of role or status. This also self-heals any chat
// that already has an empty "completed" turn sitting in its history from
// before the fix that stops persisting one (see Generate): the bad row
// stays in the database, but it's excluded here every time history gets
// rebuilt, so it can't keep breaking every future message in that chat.
// Delegates its inclusion rule to isReplayedMessage (attachment_carry_forward.go)
// rather than inlining it a second time — findCarryForwardSourceMessageID
// needs the exact same rule to determine whether an earlier turn is still
// inside the window actually replayed here, and a second, drifted copy of
// it would silently desync the two.
func toTurns(messages []chat.Message) []ai.Turn {
	turns := make([]ai.Turn, 0, len(messages))
	for _, m := range messages {
		if !isReplayedMessage(m) {
			continue
		}
		role := "user"
		if m.Role == chat.RoleAssistant {
			role = "assistant"
		}
		turns = append(turns, ai.Turn{Role: role, Content: m.Content})
	}
	return turns
}

// isUnauthorizedErr reports whether err looks like a 401 from FlowPOS.
// themefs.Store doesn't expose a structured status code for this (see
// statusErr in disk.go) — every read/write error is built as a plain
// fmt.Errorf wrapping "unexpected status %d: %s" — so this matches on that
// literal text rather than requiring a themefs API change for a single
// call site. Used to give a queued generation whose token expired before
// its turn came up a clear, specific failure message instead of a generic
// one (see doGenerate's failure defer).
func isUnauthorizedErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 401:")
}

// persistFileRecords writes the audit row for each staged/written file
// (see planToStaged/commitWritePlan) — done after the assistant message
// exists since chat_generated_files.message_id is a foreign key into it.
func (s *Service) persistFileRecords(ctx context.Context, c chat.Chat, messageID string, written []writtenFile) ([]GeneratedFile, error) {
	files := make([]GeneratedFile, 0, len(written))
	now := time.Now().UTC()
	for _, w := range written {
		kind := w.kind
		if kind == "" {
			kind = GeneratedFileKindProposed
		}
		f := GeneratedFile{
			ID:              uuid.NewString(),
			MessageID:       messageID,
			ChatID:          c.ID,
			FilePath:        w.generated.Path,
			Action:          FileAction(w.generated.Action),
			Kind:            kind,
			PageMeta:        w.pageMeta,
			Language:        languageFor(w.generated.Path),
			Content:         w.generated.Content,
			PreviousContent: w.previous,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.repo.CreateFile(ctx, f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// LoadThemeFiles fetches every render-relevant theme file's content, keyed
// by theme-relative path. store lets a caller pass a draft overlay (see
// themefs.OverlayStore) so a preview reflects unsaved changes instead of
// only the last-applied theme — the AI chat page's draft preview always
// does; Preview (the Go-renderer fidelity check) still passes s.store
// directly, unchanged, since it works from its own explicit overlay map
// instead (see handlers/preview.go).
//
// includeAssets adds .css/.js alongside .liquid — the original .liquid-only
// behavior stays available (includeAssets: false) for callers that only
// ever needed templates (nothing but a template is a render target for the
// Go engine — see liquidrender). LiquidJS's frontend preview needs CSS/JS
// too, to inline draft stylesheets/scripts (see asset_url's doc comment in
// liquid-engine.ts). pages.json is always included, regardless of
// includeAssets — a route's slug can differ from its liquid file's name
// (PageEntry.Slug vs .Page), so any caller resolving a URL to an entry file
// needs it to do that correctly.
//
// Reads run concurrently (errgroup, capped at 8 in flight) rather than one
// HTTP round trip at a time — sequential reads of a real theme's full file
// set (a few dozen files at flowpos-backend's typical latency) added up to
// multiple seconds of pure network wait, and this is now called on every
// turn (draft preview), not just once per Editor page load.
func (s *Service) LoadThemeFiles(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, includeAssets bool) (map[string]string, error) {
	tree, err := store.ListFiles(ctx, storeAuth)
	if err != nil {
		return nil, fmt.Errorf("list theme files: %w", err)
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	var wanted []string
	for path := range paths {
		if strings.HasSuffix(path, ".liquid") {
			wanted = append(wanted, path)
			continue
		}
		if includeAssets && (strings.HasSuffix(path, ".css") || strings.HasSuffix(path, ".js")) {
			wanted = append(wanted, path)
		}
	}
	// pages.json unconditionally, not gated behind includeAssets: a route's
	// slug and its actual liquid file name can differ (e.g. slug "shop" ->
	// page "products" — see PageEntry.Slug vs .Page), and any caller that
	// resolves a URL path to an entry file needs this to do it correctly
	// instead of guessing pages/<slug>.liquid, which breaks for exactly that
	// case. Harmless for callers that don't: the Go liquidrender.Renderer
	// this also feeds (handlers/preview.go) never references a "pages.json"
	// key from a {% render %}/{% include %} tag.
	if paths[pathPagesJSON] {
		wanted = append(wanted, pathPagesJSON)
	}

	files := make(map[string]string, len(wanted))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(loadThemeFilesConcurrency)
	for _, path := range wanted {
		g.Go(func() error {
			content, err := store.ReadFile(gctx, storeAuth, path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			mu.Lock()
			files[path] = content
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return files, nil
}

// LoadBaseThemeFiles is LoadThemeFiles against the real (non-overlay)
// store — a convenience for callers (handlers/preview.go) that have no
// draft-overlay reason to build one themselves and shouldn't need to know
// s.store is even a field they could reach for.
func (s *Service) LoadBaseThemeFiles(ctx context.Context, storeAuth themefs.RequestAuth, includeAssets bool) (map[string]string, error) {
	return s.LoadThemeFiles(ctx, s.store, storeAuth, includeAssets)
}

// FilesForChat returns every generated file ever written across a chat's
// whole history — used to hydrate GET /chat so reopening the page still
// shows each turn's "Generated files" card, not just the most recent one.
// Does not check ownership itself; the caller (the chat handler) has
// already scoped the chat to the requesting tenant.
func (s *Service) FilesForChat(ctx context.Context, chatID string) ([]GeneratedFile, error) {
	return s.repo.ListFilesByChat(ctx, chatID)
}

// LatestGeneration returns chatID's most recently started generation — see
// GET /chats/:chatId/stream (phase 3c), which needs to know which
// generation_id to replay events for and whether it's still running.
func (s *Service) LatestGeneration(ctx context.Context, chatID string) (Generation, error) {
	return s.repo.GetGeneration(ctx, chatID)
}

// EventsSince returns chatID's events after sinceSeq — what the stream
// handler replays before subscribing to live Redis delivery. See
// Repository.GetEventsSince's doc comment for why this is chat-scoped
// rather than tied to one generation.
func (s *Service) EventsSince(ctx context.Context, chatID string, sinceSeq int64) ([]GenerationEvent, error) {
	return s.repo.GetEventsSince(ctx, chatID, sinceSeq)
}

// SubscribeToGenerationEvents subscribes to chatID's live event bus — see
// eventBus's doc comment for the Redis-vs-in-process distinction, which is
// invisible to this method's caller (the stream handler): either way it
// gets a channel of live events and a cancel func to release it.
func (s *Service) SubscribeToGenerationEvents(ctx context.Context, chatID string) (<-chan GenerationEvent, func()) {
	return s.bus.Subscribe(ctx, chatID)
}

func languageFor(path string) string {
	switch {
	case strings.HasSuffix(path, ".liquid"):
		return "LIQUID"
	case strings.HasSuffix(path, ".css"):
		return "CSS"
	case strings.HasSuffix(path, ".js"):
		return "JS"
	case strings.HasSuffix(path, ".json"):
		return "JSON"
	case isEditableImagePath(path):
		return "IMAGE"
	default:
		return ""
	}
}

func isEditableImagePath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".webp")
}
