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

// Bounds concurrent ReadFile calls; avoids overwhelming backend/exhausting connections.
const loadThemeFilesConcurrency = 8

const (
	pathPagesJSON   = "pages.json"
	pathLayoutStart = "liquid/layout-start.liquid"
	pathLayoutEnd   = "liquid/layout-end.liquid"

	ChatType = "builder"

	// Limits repair retries to maxThemeCheckRetries+1 total Generate calls.
	maxThemeCheckRetries = 2

	// Enforced server-side; client-side cap alone is trivially bypassable.
	maxImagesPerMessage = 5

	// Server-enforced; client-side cap is trivially bypassable.
	MaxImageAttachmentBytes = 5 * 1024 * 1024

	// Pre-sanitize cap; post-sanitize is much tighter due to text token cost.
	MaxHTMLUploadBytes = 5 * 1024 * 1024

	// Post-sanitize cap: text tokens cost ~1 per 4 chars, unlike images' per-image cost.
	MaxHTMLAttachmentBytes = 300_000

	pathDefaultsJSON = "defaults.json"
)

// PostStripMaxBytes zero for no-post-sanitize kinds; byte fields are decoded/raw, never base64.
type attachmentKindLimit struct {
	MaxCount          int
	MaxBytes          int64
	PostStripMaxBytes int64
}

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

// atomic.Int64, not plain var: tests write it concurrently with background drain-loop reads (race condition).
var generateTimeoutNanos = func() *atomic.Int64 {
	var v atomic.Int64
	v.Store(int64(65 * time.Minute))
	return &v
}()

// Each drain-loop iteration gets its own fresh timeout, not shared across queue.
func generateTimeout() time.Duration { return time.Duration(generateTimeoutNanos.Load()) }

// atomic.Int64: tests write concurrently with background ticker reads (same race reason as generateTimeoutNanos).
var heartbeatTickerNanos = func() *atomic.Int64 {
	var v atomic.Int64
	v.Store(int64(heartbeatThrottle))
	return &v
}()

func heartbeatTickerInterval() time.Duration { return time.Duration(heartbeatTickerNanos.Load()) }

// Subset of *ai.Generator for testability; lets tests substitute fake without real AI provider.
type generator interface {
	Generate(ctx context.Context, tc ai.ThemeContext, history []ai.Turn, prompt string, images []ai.Image, onDelta func(string), progress ai.ToolProgress, toolExec ai.ToolExecutor, readFile ai.FileReader) (*ai.Result, error)
	SupportsVision() bool
	Summarize(ctx context.Context, turns []ai.Turn) (string, error)
}

// Private interface: lets tests substitute fake without real network calls.
type linkFetcher interface {
	Fetch(ctx context.Context, rawURL string, maxBytes int64) (urlfetch.Result, error)
	FetchStylesheets(ctx context.Context, htmlSrc string, finalURL *url.URL) (css string, count int)
}

// Service orchestrates prompt → draft staging; ApplyDraft writes to real theme (see package doc).
type Service struct {
	repo  *Repository
	chats *chat.Service
	gen   generator
	// nil in struct-literal tests; Generate treats as no reference link.
	links linkFetcher
	// nil in same tests; fetchReferenceURL falls back to uncached call.
	linkCache *referenceURLCache
	// Always REAL (non-overlay) store. Wrapping in OverlayStore per call avoids data race.
	store      themefs.ThemeStore
	themeLocks themeLocker
	bus        eventBus
	tokens     *pendingTokens
	// Not a NewService param to keep its 5-arg shape stable. Overridable via SetHistorySummarizationEnabled.
	historySummarizationEnabled bool
	historySummaries            *historySummaryCache
	historySummaryLocks         *stripedMutex
}

// Call once before serving; not safe to call concurrently with generations reading the field.
func (s *Service) SetHistorySummarizationEnabled(enabled bool) {
	s.historySummarizationEnabled = enabled
}

// rdb may be nil; events still durably written, live delivery falls back to in-process fan-out.
// store takes interface for test fakes; server.go always passes real *themefs.Store.
func NewService(repo *Repository, chats *chat.Service, gen *ai.Generator, store themefs.ThemeStore, rdb *redis.Client) *Service {
	var bus eventBus
	var locks themeLocker
	if rdb != nil {
		bus = newRedisEventBus(rdb)
		locks = newRedisThemeLock(rdb)
	} else {
		bus = newInProcessEventBus()
		// In-process lock doesn't prevent replica races; see REDIS_URL requirement in server.go.
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

// In-memory only (security); keyed by gen ID to allow either race winner to promote.
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

// Returns token and whether found, removing either way.
func (p *pendingTokens) take(generationID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	token, ok := p.tokens[generationID]
	delete(p.tokens, generationID)
	return token, ok
}

// Drops token without returning; used when queued generation is cancelled before running.
func (p *pendingTokens) discard(generationID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tokens, generationID)
}

var ErrVisionNotConfigured = errors.New("image attachments aren't enabled on this deployment")
var ErrTooManyImages = errors.New("too many images attached")
var ErrImageTooLarge = errors.New("an attached image is too large")
var ErrHTMLAttachmentTooLarge = errors.New("attached HTML file is too large")

// Wraps urlfetch error; already merchant-readable on its own.
var ErrLinkFetchFailed = errors.New("could not use the link in your message as a reference")

// Internal signal between DequeueNext and callers; never surfaced to clients (prompts are queued instead).
var ErrGenerationInProgress = errors.New("a generation is already in progress for this chat")

// One merchant prompt against the tenant's "builder" chat.
type GenerateInput struct {
	TenantID  uint64
	UserID    *uint64
	UserName  string
	UserEmail string
	Token     string
	ThemeSlug string
	Prompt    string
	// Only sent for turns in THIS generation call (initial + retries); never on later prompts.
	Images []chat.MessageImage
	// Both set: attach HTML file to turn. Carried forward from earlier turns if current has none.
	HTMLAttachmentFilename *string
	HTMLAttachmentContent  *string
	// True when content came from URL fetch vs upload (used to explain external source to model).
	HTMLAttachmentIsExternalLink bool
	// True when content came from earlier turn (model needs to know reference persists).
	HTMLAttachmentCarriedForward bool
	// True when attachment was truncated (helps model understand missing content).
	HTMLAttachmentTruncated bool
	// Detected in Prompt by Generate, validated but not fetched until doGenerate.
	ReferenceURL string
	// Set by doGenerate when fetch failed; turn still succeeds (merchant gets answer).
	ReferenceURLFetchFailed bool
	// Set when failure was ERR_BLOCKED (site refused request, not unreachable).
	ReferenceURLBlocked bool
	// Set when fetch succeeded but digest was empty (client-rendered page).
	ReferenceURLEmptyAfterSanitize bool
	// Carried through queue from RecordUserMessage so doGenerate can re-resolve images.
	UserMessageID *string
	// Restricts turn scope (brand-only, copy-only, or full edit); must be explicit, not inferred.
	Mode string
}

// Synchronous result of accepting prompt; AssistantMessage/Files always nil.
// Caller polls GET /chat for actual outcome after generation completes.
type GenerateOutcome struct {
	Chat             chat.Chat
	UserMessage      chat.Message
	AssistantMessage *chat.Message
	Files            []GeneratedFile
	// How many generations (running + queued) were ahead at moment accepted; 0 = running now.
	QueuePosition int
	// Tracks prompt; needed to cancel or correlate stream events.
	GenerationID string
}

// Background execution; queued one-at-a-time.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (GenerateOutcome, error) {
	if in.ThemeSlug == "" {
		return GenerateOutcome{}, errors.New("theme_slug is required")
	}
	imageLimit := attachmentLimits[chat.AttachmentKindImage]
	if len(in.Images) > imageLimit.MaxCount {
		return GenerateOutcome{}, fmt.Errorf("%w: at most %d images per message", ErrTooManyImages, imageLimit.MaxCount)
	}
	// Reject before persisting (fails cheaper than dequeued).
	if len(in.Images) > 0 && !s.gen.SupportsVision() {
		return GenerateOutcome{}, ErrVisionNotConfigured
	}
	// DecodedLen is cheap arithmetic; HTTP handler already validated base64.
	for i, img := range in.Images {
		if int64(base64.StdEncoding.DecodedLen(len(img.Base64))) > imageLimit.MaxBytes {
			return GenerateOutcome{}, fmt.Errorf("%w: image %d", ErrImageTooLarge, i)
		}
	}
	// Detect URL, validate shape; doGenerate does actual fetch (sync work not allowed here).
	// Upload always wins over link in prompt text (more deliberate signal).
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
		// Validate post-sanitize size (where it's actually sent to model).
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
		// Detached from request lifecycle, but bounded: each iteration gets its own generateTimeout.
		go func() {
			defer safego.Recover("themebuild.runGeneration")
			s.runGeneration(context.WithoutCancel(ctx), c, next)
		}()
	case errors.Is(err, ErrGenerationInProgress):
		// Already running; its drain loop will dequeue this row when it finishes.
		emitter := newEventEmitter(ctx, s.repo, s.bus, genID, c.ID)
		emitter.emit(ctx, EventTypeQueued, map[string]any{
			"position": position, "prompt_preview": PromptPreview(in.Prompt),
		})
	default:
		return GenerateOutcome{}, fmt.Errorf("dequeue next generation: %w", err)
	}

	return GenerateOutcome{Chat: c, UserMessage: userMsg, QueuePosition: position, GenerationID: genID}, nil
}

// Drains chatID's queue one-at-a-time starting with g; continues on failure (visible chat message).
func (s *Service) runGeneration(ctx context.Context, c chat.Chat, g Generation) {
	for {
		s.runOneQueuedGeneration(ctx, c, g)

		next, err := s.repo.DequeueNext(ctx, c.ID)
		if errors.Is(err, ErrNotFound) {
			return // queue drained
		}
		if err != nil {
			// DB error (not ErrGenerationInProgress; this loop is only thing running for c.ID).
			// Reaper's sweep will restart as orphaned.
			slog.Error("failed to dequeue next generation; the reaper will restart this chat's queue", "chat_id", c.ID, "error", err)
			return
		}
		g = next
	}
}

// Runs one already-dequeued (status=running) generation to completion and records outcome.
func (s *Service) runOneQueuedGeneration(ctx context.Context, c chat.Chat, g Generation) {
	emitter := newEventEmitter(ctx, s.repo, s.bus, g.ID, c.ID)
	emitter.emit(ctx, EventTypeDequeued, struct{}{})

	token, ok := s.tokens.take(g.ID)
	if !ok {
		// Pod restart (between enqueue and dequeue) or reaper-restarted queue has no token.
		// Expected condition (Warn not Error) but should be visible to catch spurious reaping.
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

	// Fresh timeout per iteration, not shared across queue (prevents starvation).
	workCtx, cancel := context.WithTimeout(ctx, generateTimeout())
	defer cancel()

	// Second cancel layer: for interrupting doGenerate; NOT what signals merchant stop.
	// ctx can be pre-canceled for unrelated reasons; cancelledByUser is explicit signal.
	workCtx, userCancel := context.WithCancel(workCtx)
	defer userCancel()

	// atomic: written by listener goroutine, read by doGenerate defer and tail.
	// Tells whether to emit EventTypeCancelled vs EventTypeFailed.
	var cancelledByUser atomic.Bool

	// Listens for EventTypeCancelRequested; cancels workCtx so doGenerate unwinds.
	userCancelDone := make(chan struct{})
	defer close(userCancelDone)
	cancelEvents, cancelSub := s.bus.Subscribe(context.Background(), c.ID)
	defer cancelSub()

	// Closes subscribe-after-cancel race (cancel published between DequeueNext and Subscribe).
	// Bounded: runs sync before listener/ticker/doGenerate, so unbounded call would stall startup.
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

	// Heartbeat ticker: keeps slow-but-healthy generations from being reaped mid-flight (second layer after emitLive).
	heartbeatTicker := time.NewTicker(heartbeatTickerInterval())
	defer heartbeatTicker.Stop()
	go func() {
		for {
			select {
			case <-workCtx.Done():
				return
			case <-heartbeatTicker.C:
				// Per-tick recovery: one bad tick shouldn't end heartbeats for rest of generation.
				func() {
					defer safego.Recover("themebuild.heartbeatTicker")
					// Fresh context (not workCtx): may be canceled by generation finish, harmless to lose.
					hbCtx, hbCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer hbCancel()
					if err := s.repo.UpdateGenerationHeartbeat(hbCtx, g.ID); err != nil {
						slog.Error("failed to update generation heartbeat (ticker)", "generation_id", g.ID, "error", err)
					}
					// Backstop for dropped cancel event (EventTypeCancelRequested shares bounded channel with high-frequency deltas).
					if requested, err := s.repo.IsCancellationRequested(hbCtx, c.ID, g.ID); err == nil && requested {
						cancelledByUser.Store(true)
						userCancel()
					}
				}()
			}
		}
	}()

	err := s.doGenerate(workCtx, in, c, g.ID, &cancelledByUser)

	// Fresh context: workCtx may be expired (timeout or userCancel).
	endCtx, endCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer endCancel()
	// Log error/cancellation before deciding which EndGeneration to call.
	if err != nil {
		slog.Info("generation ended", "chat_id", c.ID, "generation_id", g.ID,
			"cancelled_by_user", cancelledByUser.Load(), "error", err.Error())
	}
	// err != nil required: flag can flip true after doGenerate committed success.
	if err != nil && cancelledByUser.Load() {
		if endErr := s.repo.EndGenerationCancelled(endCtx, c.ID); endErr != nil {
			slog.Error("failed to record generation end", "chat_id", c.ID, "error", endErr)
		}
	} else if endErr := s.repo.EndGeneration(endCtx, c.ID, err); endErr != nil {
		slog.Error("failed to record generation end", "chat_id", c.ID, "error", endErr)
	}
}

// Queued generation failure when no bearer token available (see pendingTokens).
var errSessionExpired = errors.New("your session expired before this prompt ran — send it again")

// Records failed message for generation that never made it to doGenerate (e.g., no token).
func (s *Service) recordGenerationFailure(ctx context.Context, c chat.Chat, genID string, err error) {
	slog.Error("generation failed before it could start", "chat_id", c.ID, "tenant_id", c.TenantID, "error", err)
	emitter := newEventEmitter(ctx, s.repo, s.bus, genID, c.ID)
	emitter.emit(ctx, EventTypeFailed, map[string]string{"message": err.Error()})
	if _, recErr := s.chats.RecordAssistantMessage(ctx, c, err.Error(), chat.MessageStatusFailed, 0, 0, chat.ApplyStatusNotApplicable); recErr != nil {
		slog.Error("failed to record failed-generation chat message", "chat_id", c.ID, "error", recErr)
	}
}

// Fetches HTML, stylesheets, builds structured digest; cached short-TTL to avoid repeat on iteration.
// BuildDigest extracts structure (not raw HTML); SanitizeHTMLAttachment not used here.
func (s *Service) fetchReferenceURL(ctx context.Context, tenantID uint64, url string, maxBytes int64) (content string, truncated, empty bool, title string, styleCount int, err error) {
	if s.linkCache != nil {
		if cached, cachedTitle, wasTruncated, ok := s.linkCache.get(tenantID, url); ok {
			// Cache hit is never Empty (never empty by construction); styleCount stays 0 (accurate: nothing fetched THIS turn).
			return cached, wasTruncated, false, cachedTitle, 0, nil
		}
	}
	result, err := s.links.Fetch(ctx, url, maxBytes)
	if err != nil {
		return "", false, false, "", 0, err
	}
	css, styleCount := s.links.FetchStylesheets(ctx, result.HTML, result.FinalURL)
	digest := urlfetch.BuildDigest(result.FinalURL, result.HTML, css)
	content = digest.Text
	// Truncated if raw fetch cut off OR extraction alone exceeded cap.
	truncated = result.Truncated || digest.Truncated
	// PostStripMaxBytes is backstop only; digestHardCapBytes already under it.
	postStripMax := attachmentLimits[chat.AttachmentKindHTML].PostStripMaxBytes
	if int64(len(content)) > postStripMax {
		content = urlfetch.TruncateAtTagBoundary(content, postStripMax)
		truncated = true
	}
	// Empty digest (client-rendered shell) not cached: deterministic but doesn't count as success.
	if s.linkCache != nil && !digest.Empty {
		s.linkCache.set(tenantID, url, content, digest.Title, truncated)
	}
	return content, truncated, digest.Empty, digest.Title, styleCount, nil
}

// Upload path tolerance: TruncateAtTagBoundary backs up; heuristic over stored length.
// Dead path today (over-limit rejected, never truncated), kept for backstop symmetry.
const truncatedLengthTolerance = 4096

// Link path tolerance (smaller): BuildDigest trims ≤3 bytes for UTF-8; TruncateAtTagBoundary trims ≤4096.
const digestTruncatedLengthTolerance = 64

// Reports if contentLength close enough to cap to infer truncation (heuristic, not persisted fact).
func looksTruncatedByStoredLength(filename string, contentLength int64) bool {
	if looksLikeFetchedLink(filename) {
		return contentLength >= urlfetch.DigestHardCapBytes-digestTruncatedLengthTolerance
	}
	postStripMax := attachmentLimits[chat.AttachmentKindHTML].PostStripMaxBytes
	return contentLength >= postStripMax-truncatedLengthTolerance
}

// Calls AI provider for file changes; stages into draft overlay (ApplyDraft writes to real theme).
// cancelledByUser nil from tests; only runOneQueuedGeneration passes real one.
func (s *Service) doGenerate(ctx context.Context, in GenerateInput, c chat.Chat, genID string, cancelledByUser *atomic.Bool) (retErr error) {
	emitter := newEventEmitter(ctx, s.repo, s.bus, genID, c.ID)
	emitter.emit(ctx, EventTypeStarted, struct{}{})

	// summary declared here for defer closure to capture final value.
	var summary string
	// hasChanges declared here for defer closure to see final value.
	doGenerateStart := time.Now()
	var hasChanges bool
	defer func() {
		slog.Info("ai: generation wall-clock", "chat_id", c.ID, "mode", in.Mode,
			"elapsed_ms", time.Since(doGenerateStart).Milliseconds(), "has_changes", hasChanges)
	}()
	defer func() {
		// Fresh context: ctx may be expired (timeout/cancellation).
		emitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// retErr != nil required: flag can flip true after commit (immunized with commitCtx).
		if retErr != nil && cancelledByUser != nil && cancelledByUser.Load() {
			// Merchant-requested stop; no failed message (mirrors queued-prompt cancellation).
			if !errors.Is(retErr, context.Canceled) {
				slog.Error("generation failed (raced a concurrent cancel request)", "chat_id", c.ID, "tenant_id", in.TenantID, "error", retErr)
			}
			emitter.emit(emitCtx, EventTypeCancelled, map[string]string{"generation_id": genID})
			return
		}

		if retErr != nil {
			// Log raw error (never surfaced to merchant; contains provider name/URL).
			slog.Error("generation failed", "chat_id", c.ID, "tenant_id", in.TenantID, "error", retErr)
			message := ai.SanitizeError(retErr)
			if isUnauthorizedErr(retErr) {
				// Token stale before queued generation's turn; model never called.
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

	// Draft overlay: prior turns' pending changes overlaid on real theme.
	// CachingStore scoped per call: caches within one generation, not across turns.
	draft, err := s.repo.DraftFiles(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("load draft overlay: %w", err)
	}
	store := themefs.NewCachingStore(themefs.NewOverlayStore(s.store, draft))

	priorMessages, err := s.chats.ListMessages(ctx, in.TenantID, c.ID)
	if err != nil {
		return fmt.Errorf("load chat history: %w", err)
	}

	// Re-resolve images from history (in rebuilt fresh from generations row).
	// priorMessages carries metadata only; GetAttachmentsContent only runs if metadata shows attachments.
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
					// Tolerance range, not exact-cap check (see looksTruncatedByStoredLength).
					in.HTMLAttachmentTruncated = looksTruncatedByStoredLength(filename, int64(len(content)))
				default:
					slog.Warn("doGenerate: unknown attachment kind, skipping", "kind", a.Kind, "attachment_id", a.ID)
				}
			}
			break
		}
	}

	// Generate detected URL; deferred fetch now that generation runs safely in background.
	// Runs before carry-forward fallback; this turn's reference wins over earlier turn's.
	if in.HTMLAttachmentContent == nil && in.ReferenceURL != "" && s.links != nil {
		emitter.emit(ctx, EventTypeFetchingLink, map[string]string{"url": in.ReferenceURL})
		htmlLimit := attachmentLimits[chat.AttachmentKindHTML]
		digestContent, truncated, digestEmpty, title, styleCount, ferr := s.fetchReferenceURL(ctx, in.TenantID, in.ReferenceURL, htmlLimit.MaxBytes)
		// Client-rendered page (empty digest) treated as failure, not success with empty attachment.
		emptyAfterSanitize := ferr == nil && digestEmpty
		// Must NOT fail turn: merchant deserves answer about everything except the page.
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
			// Success narration (failure/empty both have their own via turn's reply).
			emitter.emit(ctx, EventTypeFetchedLink, map[string]any{"title": title, "stylesheet_count": styleCount})

			filename := in.ReferenceURL
			in.HTMLAttachmentFilename = &filename
			in.HTMLAttachmentContent = &digestContent
			in.HTMLAttachmentIsExternalLink = true
			in.HTMLAttachmentTruncated = truncated

			// Persist digest for carry-forward on later turns (filename capped to 255 for DB column width).
			// Best-effort: lost persist only costs carry-forward, must not fail generation.
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

	// Carry-forward fallback: look back for most recent earlier turn's reference within replay window.
	// in.ReferenceURL == "" required: without it, failed fetch would silently use unrelated earlier reference.
	if in.HTMLAttachmentContent == nil && in.ReferenceURL == "" {
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
				// Same heuristic as current-turn attachment check above.
				in.HTMLAttachmentTruncated = looksTruncatedByStoredLength(filename, int64(len(content)))
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

	snapBase, err := s.buildSnapshotBase(ctx, store, storeAuth)
	if err != nil {
		return fmt.Errorf("build snapshot base: %w", err)
	}

	toolExec := s.buildToolExecutor(store, storeAuth)
	readFile := s.buildFileReader(store, storeAuth)

	turns := s.summarizeOldTurnsCached(ctx, c.ID, toTurns(priorMessages))

	// Deterministic page-lifecycle ops (register existing page, diagnose failures).
	result, deterministic := s.tryDeterministicPageOp(ctx, store, storeAuth, in.Prompt)
	if !deterministic {
		var err error
		result, turns, err = s.generateValidProposal(ctx, tc, turns, in.Prompt, toolExec, readFile, emitter, in)
		if err != nil {
			return err
		}
	}

	// Fill create-without-registry gap deterministically before themecheck would reject it.
	synthesizeMissingPageRegistry(result)

	var warnings []themecheck.Finding
	// Skip check/repair for deterministic ops: no new changes to validate.
	if !deterministic && proposalHasChanges(result) {
		// Emitted for first accepted propose_changes, not repair retries (narration point already seen).
		emitter.emit(ctx, EventTypeProposing, map[string]int{"file_count": len(result.Files)})

		snap := s.buildSnapshot(ctx, store, storeAuth, snapBase, result)
		result, warnings, err = s.checkAndRepair(ctx, in, c.ID, tc, turns, result, snap, toolExec, readFile, emitter)
		if err != nil {
			return err
		}
	}

	// Final gate: never silently delete/unregister protected pages (blog, home).
	var blockedPages []string
	result, blockedPages = protectPages(result, in.Prompt, tc.PagesJSON)

	hasChanges = proposalHasChanges(result)

	var staged []writtenFile
	if hasChanges {
		// Lock for buildWritePlan (reads layout files); concurrent generations could race.
		unlock, err := s.themeLocks.Lock(ctx, themeLockKey(in.TenantID, in.ThemeSlug))
		if err != nil {
			return fmt.Errorf("stage theme changes: %w", err)
		}
		defer unlock()

		// Computed entirely in memory; failure leaves draft untouched (not half-staged).
		plan, err := s.buildWritePlan(ctx, store, storeAuth, result)
		if err != nil {
			return fmt.Errorf("stage theme changes: %w", err)
		}
		emitter.emit(ctx, EventTypeStaged, map[string]any{"paths": plan.paths()})

		// Draft/apply split: nothing reaches FlowPOS here (only staged, not committed).
		staged = planToStaged(plan)
	}

	applyStatus := chat.ApplyStatusNotApplicable
	if hasChanges {
		applyStatus = chat.ApplyStatusPending
	}

	// Empty summary with cache_control breakpoint breaks all future messages (Anthropic rejects it).
	summary = result.Summary
	if summary == "" {
		summary = "Done."
	}
	summary = appendWarningsNote(summary, warnings)
	summary = protectedPagesNote(summary, blockedPages)

	// Detached commitCtx: cancel can only stop BEFORE this point, never mid-commit.
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

// Reports if chatID has running generation and error from most recent failure (cleared on next start).
// Backed by generations table; survives pod restart and works with multiple replicas.
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

// *themefs.Store-only capability; manifest cached against real theme, not draft.
type manifestGenerator interface {
	GetOrGenerateManifest(ctx context.Context, auth themefs.RequestAuth) (themefs.Manifest, error)
}

// *themefs.Store-only capability; ThemeStore.ReadFile returns string (corrupts binary).
type rawAssetReader interface {
	ReadFileBytes(ctx context.Context, auth themefs.RequestAuth, relPath string) ([]byte, error)
}

// *themefs.Store-only capability; store settings aren't theme files.
type storeSettingsFetcher interface {
	FetchStoreSettings(ctx context.Context, auth themefs.RequestAuth) (themefs.StoreSettings, error)
}

// *themefs.Store-only capability; products list isn't theme files.
type productsFetcher interface {
	FetchProducts(ctx context.Context, auth themefs.RequestAuth, limit int) (themefs.ProductsPage, error)
}

// Fetches real published products for preview (instead of sample data).
func (s *Service) FetchPreviewProducts(ctx context.Context, storeAuth themefs.RequestAuth, limit int) (themefs.ProductsPage, error) {
	fetcher, ok := s.store.(productsFetcher)
	if !ok {
		return themefs.ProductsPage{}, fmt.Errorf("theme store does not support products fetch")
	}
	return fetcher.FetchProducts(ctx, storeAuth, limit)
}

// Fetches real store settings (name) for preview (instead of sample data).
func (s *Service) FetchStoreSettings(ctx context.Context, storeAuth themefs.RequestAuth) (themefs.StoreSettings, error) {
	fetcher, ok := s.store.(storeSettingsFetcher)
	if !ok {
		return themefs.StoreSettings{}, fmt.Errorf("theme store does not support store settings fetch")
	}
	return fetcher.FetchStoreSettings(ctx, storeAuth)
}

// Reads real defaults.json menu object for preview (instead of static fallback).
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

// Fetches theme file's raw bytes for AssetHandler (always real theme, never draft).
func (s *Service) ReadThemeAssetBytes(ctx context.Context, storeAuth themefs.RequestAuth, relPath string) ([]byte, error) {
	reader, ok := s.store.(rawAssetReader)
	if !ok {
		return nil, fmt.Errorf("theme store does not support raw asset reads")
	}
	return reader.ReadFileBytes(ctx, storeAuth, relPath)
}

// Four independent store round trips run concurrently; each goroutine owns its variable (no mutex).
func (s *Service) buildThemeContext(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, themeSlug string) (ai.ThemeContext, error) {
	var pagesJSON, defaultsJSON string
	var tree []themefs.FileTreeEntry
	var manifest themefs.Manifest

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { pagesJSON, err = store.ReadFile(gctx, storeAuth, pathPagesJSON); return })
	g.Go(func() (err error) { defaultsJSON, err = store.ReadFile(gctx, storeAuth, pathDefaultsJSON); return })
	g.Go(func() (err error) { tree, err = store.ListFiles(gctx, storeAuth); return })
	g.Go(func() error {
		mg, ok := s.store.(manifestGenerator)
		if !ok {
			return nil
		}
		m, err := mg.GetOrGenerateManifest(gctx, storeAuth)
		if err != nil {
			return fmt.Errorf("build manifest: %w", err)
		}
		manifest = m
		return nil
	})
	if err := g.Wait(); err != nil {
		return ai.ThemeContext{}, err
	}

	return ai.ThemeContext{
		ThemeSlug:    themeSlug,
		PagesJSON:    pagesJSON,
		DefaultsJSON: defaultsJSON,
		FileTree:     tree,
		Manifest:     &manifest,
	}, nil
}

// Fetches invariant snapshot part: file-path listing + content for files themecheck reads.
// Split from buildSnapshot for independent testability; lets future callers reuse base.
func (s *Service) buildSnapshotBase(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth) (themecheck.Snapshot, error) {
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
	return themecheck.Snapshot{Files: files, Paths: paths}, nil
}

// Layers real content of proposed-update files on top of base (for placeholder/pre-existing checks).
// Fetches fail open; base built once before check-repair loop, valid across retries.
func (s *Service) buildSnapshot(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, base themecheck.Snapshot, result *ai.Result) themecheck.Snapshot {
	files := make(map[string]string, len(base.Files)+len(result.Files))
	for k, v := range base.Files {
		files[k] = v
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
			// Fails open: this backs optional checks (placeholder, pre-existing); miss reverts to stricter behavior.
			slog.Warn("failed to fetch baseline content for proposed file; treating it as having no baseline",
				"path", f.Path, "error", err)
			continue
		}
		files[f.Path] = content
	}

	return themecheck.Snapshot{Files: files, Paths: base.Paths}
}

// Walks file tree, recording every FILE path (not directories).
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

// Replays chat history as turns; skips empty turns (Anthropic rejects empty text blocks).
// Delegates inclusion to isReplayedMessage to keep carry-forward sync'd.
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

// Reports if err looks like a 401 from FlowPOS (matches literal text; no structured status).
func isUnauthorizedErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 401:")
}

// Writes audit row for each staged file; done after assistant message (FK constraint).
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

// Fetches render-relevant theme files (liquid + optional css/js + pages.json); concurrent reads capped at 8.
// store lets caller pass draft overlay (AI chat does; preview fidelity check doesn't).
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
	// pages.json unconditionally (slug ≠ liquid filename; needed for URL→entry resolution).
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

// LoadThemeFiles against real (non-overlay) store; convenience to hide s.store field.
func (s *Service) LoadBaseThemeFiles(ctx context.Context, storeAuth themefs.RequestAuth, includeAssets bool) (map[string]string, error) {
	return s.LoadThemeFiles(ctx, s.store, storeAuth, includeAssets)
}

// Returns all generated files across chat's history for GET /chat hydration.
// Caller already scoped chat to requesting tenant (no ownership check needed).
func (s *Service) FilesForChat(ctx context.Context, chatID string) ([]GeneratedFile, error) {
	return s.repo.ListFilesByChat(ctx, chatID)
}

// Returns most recently started generation (needed to replay stream events and check status).
func (s *Service) LatestGeneration(ctx context.Context, chatID string) (Generation, error) {
	return s.repo.GetGeneration(ctx, chatID)
}

// Returns events after sinceSeq (replayed by stream handler before live subscription).
func (s *Service) EventsSince(ctx context.Context, chatID string, sinceSeq int64) ([]GenerationEvent, error) {
	return s.repo.GetEventsSince(ctx, chatID, sinceSeq)
}

// Subscribes to live event bus (Redis-vs-in-process transparent to caller).
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
