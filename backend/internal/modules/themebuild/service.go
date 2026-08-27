package themebuild

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/safego"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"

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

	pathDefaultsJSON = "defaults.json"
)

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
	Generate(ctx context.Context, tc ai.ThemeContext, history []ai.Turn, prompt string, onDelta func(string), progress ai.ToolProgress, toolExec ai.ToolExecutor) (*ai.Result, error)
	// Summarize is used by summarizeOldTurns to collapse old chat history
	// into one synthetic turn instead of resending it verbatim on every
	// call — see summarizeOldTurns's doc comment. *ai.Generator's fake mode
	// (see ai.NewFake) implements this with a cheap deterministic string
	// and never calls the real API, matching Generate's own fake-mode
	// convention.
	Summarize(ctx context.Context, turns []ai.Turn) (string, error)
}

// Service is the AI theme builder's orchestration: turn a prompt into
// proposed changes and stage them into the chat's draft overlay (see
// Generate) — writing to the real theme is a separate, explicit ApplyDraft
// step (see this package's own doc comment for the draft/apply split).
type Service struct {
	repo  *Repository
	chats *chat.Service
	gen   generator
	// store is always the REAL (non-overlay) store — see doGenerate, which
	// wraps it in a fresh themefs.OverlayStore per generation call rather
	// than mutating this field. A mutable "current store" field here would
	// be a data race: this Service is shared across every concurrent
	// generation for every chat/tenant, and each one's draft is its own.
	store      themefs.ThemeStore
	themeLocks themeLocker // redisThemeLock if REDIS_URL was configured, keyedMutex otherwise — see themelock.go
	bus        eventBus    // redisEventBus if REDIS_URL was configured, inProcessEventBus otherwise — see eventEmitter
	tokens     *pendingTokens
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
		repo:       repo,
		chats:      chats,
		gen:        gen,
		store:      store,
		themeLocks: locks,
		bus:        bus,
		tokens:     newPendingTokens(),
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

	c, err := s.chats.GetOrCreateChat(ctx, in.TenantID, ChatType)
	if err != nil {
		return GenerateOutcome{}, err
	}

	userMsg, err := s.chats.RecordUserMessage(ctx, c, in.UserID, in.UserName, in.UserEmail, in.Prompt)
	if err != nil {
		return GenerateOutcome{}, fmt.Errorf("record user message: %w", err)
	}

	genID := uuid.NewString()
	position, err := s.repo.EnqueueGeneration(ctx, Generation{
		ID:            genID,
		ChatID:        c.ID,
		TenantID:      in.TenantID,
		Prompt:        in.Prompt,
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
		TenantID:  g.TenantID,
		Token:     token,
		ThemeSlug: g.ThemeSlug,
		Prompt:    g.Prompt,
		Mode:      g.Mode,
	}

	// Each drain-loop iteration gets its own fresh timeout — one shared
	// deadline across a queue of five prompts would starve the later ones
	// of their fair share of generateTimeout, or worse, kill them
	// mid-generation through no fault of their own. ctx itself is already
	// context.WithoutCancel of the original request (see Generate), so it
	// outlives any one HTTP call without being unbounded itself.
	workCtx, cancel := context.WithTimeout(ctx, generateTimeout())
	defer cancel()

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
				}()
			}
		}
	}()

	err := s.doGenerate(workCtx, in, c, g.ID)

	// A deliberately fresh, short-lived context for this one bookkeeping
	// write: workCtx may already be expired (a generation that hit
	// generateTimeout), and the outcome still needs recording either way.
	endCtx, endCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer endCancel()
	if endErr := s.repo.EndGeneration(endCtx, c.ID, err); endErr != nil {
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

// doGenerate is the part of generation that used to be Generate's entire
// body before it became async: ask Claude for the resulting file changes
// and stage them into the chat's draft overlay — see this package's own
// doc comment for the draft/apply split; writing to the real theme is a
// separate, explicit Service.ApplyDraft step, not something this does.
func (s *Service) doGenerate(ctx context.Context, in GenerateInput, c chat.Chat, genID string) (retErr error) {
	emitter := newEventEmitter(ctx, s.repo, s.bus, genID, c.ID)
	emitter.emit(ctx, EventTypeStarted, struct{}{})

	// summary is declared here (not with := at its point of use below) so
	// this defer's closure captures the same variable and sees its final
	// value — a "done" event needs the actual summary, not a placeholder.
	var summary string
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

	tc, err := s.buildThemeContext(ctx, store, storeAuth, in.ThemeSlug)
	if err != nil {
		return fmt.Errorf("load theme context: %w", err)
	}
	tc.GenerationMode = in.Mode

	toolExec := s.buildToolExecutor(store, storeAuth)

	turns := summarizeOldTurns(ctx, s.gen, toTurns(priorMessages))
	result, turns, err := s.generateValidProposal(ctx, tc, turns, in.Prompt, toolExec, emitter, in)
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
		result, warnings, err = s.checkAndRepair(ctx, in, c.ID, tc, turns, result, snap, toolExec, emitter)
		if err != nil {
			return err
		}
	}

	hasChanges := proposalHasChanges(result)

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
		unlock, err := s.themeLocks.Lock(ctx, in.ThemeSlug)
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

	assistantMsg, err := s.chats.RecordAssistantMessage(ctx, c, summary, chat.MessageStatusCompleted, result.InputTokens, result.OutputTokens, applyStatus)
	if err != nil {
		return fmt.Errorf("record assistant message: %w", err)
	}

	if _, err := s.persistFileRecords(ctx, c, assistantMsg.ID, staged); err != nil {
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
// with). Called once per doGenerate call, before the check-and-repair
// loop: nothing is written to the theme until after that loop accepts a
// proposal, so the same snapshot is valid across every retry within one
// call — no need to refetch it per attempt, even though result itself may
// be replaced by a retried proposal (checkAndRepair keeps re-using this
// same snapshot; only fresh update paths that first appear on a retry
// would miss a "before" here, same as before this change for any path).
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
			return themecheck.Snapshot{}, fmt.Errorf("read %s: %w", f.Path, err)
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
func toTurns(messages []chat.Message) []ai.Turn {
	turns := make([]ai.Turn, 0, len(messages))
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		switch m.Role {
		case chat.RoleUser:
			turns = append(turns, ai.Turn{Role: "user", Content: m.Content})
		case chat.RoleAssistant:
			if m.Status == chat.MessageStatusCompleted {
				turns = append(turns, ai.Turn{Role: "assistant", Content: m.Content})
			}
		}
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

// CreateThemeFromBase creates a brand-new theme for the tenant from
// flowpos-backend's base-theme catalog entry and returns its slug — the
// entry point for the "start a theme from scratch" flow, before any chat
// message exists for it (see themefs.Store.CreateThemeFromBase).
func (s *Service) CreateThemeFromBase(ctx context.Context, tenantID uint64, token string) (string, error) {
	slug, err := s.store.CreateThemeFromBase(ctx, themefs.RequestAuth{Token: token, TenantID: tenantID})
	if err != nil {
		return "", fmt.Errorf("create theme from base: %w", err)
	}
	return slug, nil
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
// liquid-engine.ts).
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
	default:
		return ""
	}
}
