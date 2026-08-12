package themebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

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
// proposed changes and write them straight to the real theme filesystem
// (see Generate) — there is no separate review/apply step.
type Service struct {
	repo       *Repository
	chats      *chat.Service
	gen        generator
	store      *themefs.Store
	themeLocks *keyedMutex
	bus        eventBus // redisEventBus if REDIS_URL was configured, inProcessEventBus otherwise — see eventEmitter
	tokens     *pendingTokens
}

// NewService wires the service's dependencies. rdb may be nil (see
// NewRedisClient) — generation events are then still durably written to
// generation_events, and live delivery falls back to an in-process fan-out
// (see eventbus.go) that only reaches a WebSocket connected to this same
// replica.
func NewService(repo *Repository, chats *chat.Service, gen *ai.Generator, store *themefs.Store, rdb *redis.Client) *Service {
	var bus eventBus
	if rdb != nil {
		bus = newRedisEventBus(rdb)
	} else {
		bus = newInProcessEventBus()
	}
	return &Service{
		repo:       repo,
		chats:      chats,
		gen:        gen,
		store:      store,
		themeLocks: newKeyedMutex(),
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
// model proposed changes) write to the real theme filesystem all happen in
// a background goroutine (see runGeneration), not before this returns.
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
// A model/infra failure, a rejected proposal, or a failure while writing to
// the theme is never persisted as a chat turn — errors are request-scoped,
// not chat history (see the generations table, phase 3a) — same reasoning
// as before this became async, just durable now instead of in-memory.
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
		go s.runGeneration(context.WithoutCancel(ctx), c, next)
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
// and — unlike an earlier pending/apply design — write them to the real
// theme filesystem immediately, with no separate "Apply to theme" step.
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

	priorMessages, err := s.chats.ListMessages(ctx, in.TenantID, c.ID)
	if err != nil {
		return fmt.Errorf("load chat history: %w", err)
	}

	tc, err := s.buildThemeContext(ctx, storeAuth, in.ThemeSlug)
	if err != nil {
		return fmt.Errorf("load theme context: %w", err)
	}
	tc.GenerationMode = in.Mode

	toolExec := s.buildToolExecutor(storeAuth)

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

		snap, err := s.buildSnapshot(ctx, storeAuth, result)
		if err != nil {
			return fmt.Errorf("build theme snapshot: %w", err)
		}
		result, warnings, err = s.checkAndRepair(ctx, in, c.ID, tc, turns, result, snap, toolExec, emitter)
		if err != nil {
			return err
		}
	}

	hasChanges := proposalHasChanges(result)

	var written []writtenFile
	if hasChanges {
		// Serializes the read-modify-write critical section below per
		// theme: two concurrent requests for the same theme (two tabs, a
		// client retry racing the original) must not interleave reads and
		// writes of pages.json/the layout files, or one's registration can
		// silently clobber the other's (a lost update). Scoped tightly to
		// just this section, not the whole call — the Claude call above
		// can take minutes, and a second tab's edit shouldn't queue behind
		// that, only behind the fast disk work.
		unlock := s.themeLocks.Lock(in.ThemeSlug)
		defer unlock()

		// Computed entirely in memory first, nothing written yet: a
		// failure here (e.g. a duplicate page slug) leaves the real theme
		// completely untouched, rather than a validation error arriving
		// after some files already landed on disk with nothing recording
		// that they did.
		plan, err := s.buildWritePlan(ctx, storeAuth, result)
		if err != nil {
			return fmt.Errorf("apply to theme: %w", err)
		}
		emitter.emit(ctx, EventTypeStaged, map[string]any{"paths": plan.paths()})

		written, err = s.commitWritePlan(ctx, storeAuth, plan)
		if err != nil {
			return fmt.Errorf("apply to theme: %w", err)
		}
	}

	applyStatus := chat.ApplyStatusNotApplicable
	if hasChanges {
		applyStatus = chat.ApplyStatusApplied
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

	if _, err := s.persistFileRecords(ctx, c, assistantMsg.ID, written); err != nil {
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

func (s *Service) buildThemeContext(ctx context.Context, storeAuth themefs.RequestAuth, themeSlug string) (ai.ThemeContext, error) {
	pagesJSON, err := s.store.ReadFile(ctx, storeAuth, pathPagesJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	defaultsJSON, err := s.store.ReadFile(ctx, storeAuth, pathDefaultsJSON)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	tree, err := s.store.ListFiles(ctx, storeAuth)
	if err != nil {
		return ai.ThemeContext{}, err
	}
	manifest, err := s.store.GetOrGenerateManifest(ctx, storeAuth)
	if err != nil {
		return ai.ThemeContext{}, fmt.Errorf("build manifest: %w", err)
	}
	return ai.ThemeContext{
		ThemeSlug:    themeSlug,
		PagesJSON:    pagesJSON,
		DefaultsJSON: defaultsJSON,
		FileTree:     tree,
		Manifest:     &manifest,
	}, nil
}

// maxToolReadPaths/maxToolReadBytes bound read_theme_file's own single-call
// footprint — independent of maxEditingFiles-style history bounds (there
// are none anymore, see doc comment on the deleted buildEditingFilesContext):
// the model now asks for exactly the files it wants, so the only thing to
// bound is one call's own size.
const (
	maxToolReadPaths    = 10
	maxToolReadBytes    = 40_000
	maxGrepMatches      = 200
	maxGrepFilesScanned = 500
)

// grepThemeSearchableExt is the set of file types grep_theme will search —
// theme-relative text files only; images/fonts etc. are never candidates.
var grepThemeSearchableExt = map[string]bool{".liquid": true, ".css": true, ".js": true, ".json": true}

// buildToolExecutor returns the ai.ToolExecutor this generation call uses
// to read the real theme — the only place ai.Generate ever reaches
// themefs, and only through this closure (see ai.ToolExecutor's doc
// comment): package ai never imports themefs's Store directly.
func (s *Service) buildToolExecutor(storeAuth themefs.RequestAuth) ai.ToolExecutor {
	return func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		switch name {
		case "list_theme_files":
			return s.execListThemeFiles(ctx, storeAuth)
		case "read_theme_file":
			return s.execReadThemeFile(ctx, storeAuth, input)
		case "grep_theme":
			return s.execGrepTheme(ctx, storeAuth, input)
		default:
			return "", fmt.Errorf("unknown tool %q", name)
		}
	}
}

func (s *Service) execListThemeFiles(ctx context.Context, storeAuth themefs.RequestAuth) (string, error) {
	tree, err := s.store.ListFiles(ctx, storeAuth)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(tree)
	if err != nil {
		return "", fmt.Errorf("encode file tree: %w", err)
	}
	return string(encoded), nil
}

type readThemeFileInput struct {
	Paths []string `json:"paths"`
}

// execReadThemeFile reads up to maxToolReadPaths files, capping the total
// content returned at maxToolReadBytes — a model asking for several large
// files in one call gets a clear truncation marker rather than a silently
// cut-off response it might mistake for the whole file.
func (s *Service) execReadThemeFile(ctx context.Context, storeAuth themefs.RequestAuth, input json.RawMessage) (string, error) {
	var args readThemeFileInput
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid read_theme_file input: %w", err)
	}
	if len(args.Paths) == 0 {
		return "", fmt.Errorf("paths must not be empty")
	}
	if len(args.Paths) > maxToolReadPaths {
		args.Paths = args.Paths[:maxToolReadPaths]
	}

	var b strings.Builder
	total := 0
	for _, p := range args.Paths {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %s\n\n", p, err.Error())
			continue
		}
		content, err := s.store.ReadFile(ctx, storeAuth, p)
		if err != nil {
			fmt.Fprintf(&b, "### %s\nERROR: %s\n\n", p, err.Error())
			continue
		}
		if content == "" {
			fmt.Fprintf(&b, "### %s\n(does not exist yet)\n\n", p)
			continue
		}
		if total+len(content) > maxToolReadBytes {
			fmt.Fprintf(&b, "(remaining files omitted — total content capped at %d bytes per call; read fewer files per call)\n", maxToolReadBytes)
			break
		}
		total += len(content)
		fmt.Fprintf(&b, "### %s\n%s\n\n", p, content)
	}
	return b.String(), nil
}

type grepThemeInput struct {
	Pattern  string `json:"pattern"`
	PathGlob string `json:"path_glob"`
}

// execGrepTheme searches every searchable theme file for a regular
// expression (RE2 — Go's regexp package, not a plain substring; see
// grepThemeTool's description) matched line-by-line, optionally restricted
// to paths matching path_glob (path.Match — one wildcard segment, no "**").
func (s *Service) execGrepTheme(ctx context.Context, storeAuth themefs.RequestAuth, input json.RawMessage) (string, error) {
	var args grepThemeInput
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid grep_theme input: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern must not be empty")
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}

	tree, err := s.store.ListFiles(ctx, storeAuth)
	if err != nil {
		return "", err
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	candidates := make([]string, 0, len(paths))
	for p := range paths {
		if !grepThemeSearchableExt[path.Ext(p)] {
			continue
		}
		if args.PathGlob != "" {
			matched, globErr := path.Match(args.PathGlob, p)
			if globErr != nil {
				return "", fmt.Errorf("invalid path_glob: %w", globErr)
			}
			if !matched {
				continue
			}
		}
		candidates = append(candidates, p)
	}
	sort.Strings(candidates)
	if len(candidates) > maxGrepFilesScanned {
		candidates = candidates[:maxGrepFilesScanned]
	}

	var b strings.Builder
	matches := 0
	for _, p := range candidates {
		if matches >= maxGrepMatches {
			break
		}
		content, err := s.store.ReadFile(ctx, storeAuth, p)
		if err != nil || content == "" {
			continue
		}
		for i, line := range strings.Split(content, "\n") {
			if matches >= maxGrepMatches {
				break
			}
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d: %s\n", p, i+1, strings.TrimSpace(line))
				matches++
			}
		}
	}
	if matches == 0 {
		return "(no matches)", nil
	}
	if matches >= maxGrepMatches {
		fmt.Fprintf(&b, "(stopped at %d matches — narrow your pattern/path_glob)\n", maxGrepMatches)
	}
	return b.String(), nil
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
func (s *Service) buildSnapshot(ctx context.Context, storeAuth themefs.RequestAuth, result *ai.Result) (themecheck.Snapshot, error) {
	tree, err := s.store.ListFiles(ctx, storeAuth)
	if err != nil {
		return themecheck.Snapshot{}, fmt.Errorf("list theme files: %w", err)
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	files := make(map[string]string, 4)
	for _, path := range []string{pathPagesJSON, pathDefaultsJSON, pathLayoutStart, pathLayoutEnd} {
		content, err := s.store.ReadFile(ctx, storeAuth, path)
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
		content, err := s.store.ReadFile(ctx, storeAuth, f.Path)
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

// toProposal maps ai.Result into the minimal shape themecheck.Check needs —
// PageRegistryEntry carries over unchanged since ai.Result already types it
// as *themefs.PageEntry (see themecheck.Proposal's doc comment).
func toProposal(r *ai.Result) themecheck.Proposal {
	files := make([]themecheck.ProposedFile, len(r.Files))
	for i, f := range r.Files {
		files[i] = themecheck.ProposedFile{Path: f.Path, Action: f.Action, Content: f.Content}
	}
	return themecheck.Proposal{
		Files:              files,
		PageRegistryEntry:  r.PageRegistryEntry,
		LayoutLinksToAdd:   r.LayoutLinksToAdd,
		LayoutScriptsToAdd: r.LayoutScriptsToAdd,
	}
}

// proposalHasChanges reports whether result proposes anything to write —
// shared by doGenerate (deciding whether to run themecheck/buildWritePlan at
// all) and its post-repair recheck (a retry that ends in NeedsClarification
// legitimately has nothing left to write).
func proposalHasChanges(result *ai.Result) bool {
	return len(result.Files) > 0 || result.PageRegistryEntry != nil ||
		len(result.LayoutLinksToAdd) > 0 || len(result.LayoutScriptsToAdd) > 0
}

// clearIfNeedsClarification defensively drops any changes the model
// proposed despite the system prompt's instruction not to when asking a
// clarifying question — a clarification turn has nothing to apply.
func clearIfNeedsClarification(result *ai.Result) {
	if !result.NeedsClarification {
		return
	}
	result.Files = nil
	result.PageRegistryEntry = nil
	result.LayoutLinksToAdd = nil
	result.LayoutScriptsToAdd = nil
}

// generateValidProposal makes doGenerate's very first Generate call and
// retries it, up to maxThemeCheckRetries times, if the reply fails
// validateProposal — the same bounded treatment checkAndRepair's own retry
// loop applies to a malformed *repair* reply (see its invalid-proposal
// branch), now covering the first attempt too: a garbled first reply (a bad
// path, a corrupted field) is model flakiness observed in production, not a
// reason to hard-fail the whole generation with zero retries. Returns the
// (possibly extended) turns history so a later checkAndRepair call sees any
// corrective exchange that happened here, instead of silently dropping it.
func (s *Service) generateValidProposal(
	ctx context.Context,
	tc ai.ThemeContext,
	turns []ai.Turn,
	prompt string,
	toolExec ai.ToolExecutor,
	emitter *eventEmitter,
	in GenerateInput,
) (*ai.Result, []ai.Turn, error) {
	nextPrompt := prompt

	// attempt counts total Generate calls made here, including the first —
	// mirrors checkAndRepair's own budget: maxThemeCheckRetries+1 total
	// calls (the original attempt plus this many retries), independent of
	// checkAndRepair's own separate retry budget for themecheck rejections
	// (see doGenerate's structure: these are two distinct stages).
	for attempt := 1; ; attempt++ {
		result, genErr := s.gen.Generate(ctx, tc, turns, nextPrompt, onThinkingDelta(ctx, emitter), toolProgressFor(ctx, emitter), toolExec)
		if genErr != nil {
			// A hard API/transport error is a different failure mode from an
			// invalid proposal — already handled by the caller/reaper, not
			// retried here.
			return nil, turns, genErr
		}

		clearIfNeedsClarification(result)
		if err := validateProposal(result, tc.GenerationMode); err == nil {
			return result, turns, nil
		} else if attempt >= maxThemeCheckRetries+1 {
			return nil, turns, fmt.Errorf("invalid model proposal: %w", err)
		} else {
			slog.Warn("initial generation produced an invalid proposal, retrying if budget remains",
				"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt, "error", err)
			emitter.emit(ctx, EventTypeCheckFailed, map[string]any{
				"attempt": attempt, "message": "invalid model proposal: " + err.Error(),
			})
			turns = append(turns,
				ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)},
				ai.Turn{Role: "user", Content: fmt.Sprintf(
					"That reply wasn't valid: %s. Resubmit a corrected, complete proposal (not a diff), "+
						"following the earlier instructions exactly. Never invent a placeholder path or "+
						"partial content — if you don't have a complete, verified proposal ready, call "+
						"propose_changes with needs_clarification: true, files: [], and explain why instead "+
						"of guessing.", err)},
			)
			nextPrompt = "Please resubmit a corrected, complete proposal as instructed above."
		}
	}
}

// checkAndRepair validates result against snap via themecheck.Check. A
// blocking (error-severity) finding is fed back to the model as a new
// assistant/user turn pair and retried, up to maxThemeCheckRetries times;
// a proposal that never passes fails the generation with a merchant-
// friendly message. Token usage from every retry is folded into the
// accepted result's totals, so RecordAssistantMessage still bills/records
// the full cost of this turn, not just its last attempt. The accepted
// result's warning findings are returned alongside it — never blocking,
// just surfaced (see doGenerate's appendWarningsNote).
func (s *Service) checkAndRepair(
	ctx context.Context,
	in GenerateInput,
	chatID string,
	tc ai.ThemeContext,
	history []ai.Turn,
	result *ai.Result,
	snap themecheck.Snapshot,
	toolExec ai.ToolExecutor,
	emitter *eventEmitter,
) (*ai.Result, []themecheck.Finding, error) {
	turns := append([]ai.Turn(nil), history...)
	totalInput, totalOutput := result.InputTokens, result.OutputTokens

	for attempt := 1; ; attempt++ {
		// Best-effort: recorded so retry frequency is measurable via the
		// generations table, not just logs — never worth failing the whole
		// generation over. s.repo is nil only in tests that construct a
		// Service directly around a fake generator (see check_and_repair_test.go).
		if s.repo != nil {
			if err := s.repo.SetGenerationAttempts(ctx, chatID, attempt); err != nil {
				slog.Warn("failed to record generation attempt count", "chat_id", chatID, "error", err)
			}
		}

		emitter.emit(ctx, EventTypeChecking, map[string]int{"attempt": attempt})
		findings := themecheck.Check(toProposal(result), snap)
		errorFindings, warningFindings := splitFindings(findings)

		// A missing layout-start/layout-end render is mechanical, not a
		// judgment call — the required text is fixed and known, so patch it
		// in directly rather than spending a whole model round-trip asking
		// for something it has already failed to add correctly at least
		// twice in production (see AutoFixMissingBoilerplate's doc comment).
		// Free (no extra Generate call): just re-run Check on the patched
		// content before deciding whether a real repair round-trip is
		// needed at all.
		if len(errorFindings) > 0 {
			fixedAny := false
			if fixedContent, any := themecheck.AutoFixMissingBoilerplate(toProposal(result)); any {
				for i, f := range result.Files {
					if patched, ok := fixedContent[f.Path]; ok {
						result.Files[i].Content = patched
					}
				}
				fixedAny = true
			}
			// A proposed css/js file with no matching <link>/<script>
			// registration is equally mechanical — see
			// themecheck.AutoFixMissingAssetRegistration's doc comment.
			if links, scripts, any := themecheck.AutoFixMissingAssetRegistration(toProposal(result), snap); any {
				result.LayoutLinksToAdd = append(result.LayoutLinksToAdd, links...)
				result.LayoutScriptsToAdd = append(result.LayoutScriptsToAdd, scripts...)
				fixedAny = true
			}
			if fixedAny {
				findings = themecheck.Check(toProposal(result), snap)
				errorFindings, warningFindings = splitFindings(findings)
			}
		}

		if len(errorFindings) == 0 {
			if attempt > 1 {
				slog.Info("themecheck accepted proposal after retry",
					"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt, "warning_count", len(warningFindings))
			}
			result.InputTokens, result.OutputTokens = totalInput, totalOutput
			return result, warningFindings, nil
		}

		slog.Warn("themecheck rejected proposal",
			"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt,
			"error_count", len(errorFindings), "rules", findingRules(errorFindings))
		emitter.emit(ctx, EventTypeCheckFailed, map[string]any{"findings": errorFindings, "attempt": attempt})

		if attempt > maxThemeCheckRetries {
			return nil, nil, fmt.Errorf("the generated changes didn't pass validation after %d attempts: %s",
				attempt, summarizeFindings(errorFindings))
		}

		emitter.emit(ctx, EventTypeRepairing, map[string]int{"attempt": attempt})
		turns = append(turns, ai.Turn{Role: "assistant", Content: recapAssistantTurn(result)})
		repair := repairPrompt(errorFindings)

		repairStart := time.Now()
		retried, genErr := s.gen.Generate(ctx, tc, turns, repair, onThinkingDelta(ctx, emitter), toolProgressFor(ctx, emitter), toolExec)
		repairElapsed := time.Since(repairStart)
		if genErr != nil {
			// Surfaced distinctly from the generic reaper cleanup: without
			// this, a repair call that runs out the remaining generateTimeout
			// budget (ctx canceled mid-call) produces no log of its own —
			// the chat just sits on "repairing" until the reaper's 1-minute
			// sweep marks it failed, with nothing in the logs explaining why.
			slog.Error("repair generation failed", "tenant_id", in.TenantID, "theme_slug", in.ThemeSlug,
				"attempt", attempt, "elapsed", repairElapsed, "error", genErr)
			return nil, nil, fmt.Errorf("retry generation: %w", genErr)
		}
		slog.Info("repair generation completed", "tenant_id", in.TenantID, "theme_slug", in.ThemeSlug,
			"attempt", attempt, "elapsed", repairElapsed, "input_tokens", retried.InputTokens, "output_tokens", retried.OutputTokens)
		totalInput += retried.InputTokens
		totalOutput += retried.OutputTokens
		turns = append(turns, ai.Turn{Role: "user", Content: repair})

		clearIfNeedsClarification(retried)
		if err := validateProposal(retried, tc.GenerationMode); err != nil {
			// A malformed repair reply (garbled path, corrupted JSON field,
			// etc.) is model flakiness, not necessarily a dead end — the
			// SAME retry budget that governs themecheck rejections should
			// cover this too, instead of burning the whole generation on
			// one bad roll of the dice. `result` (the last themecheck-
			// rejected-but-well-formed proposal) is deliberately left
			// unchanged here so the next loop iteration re-runs Check on
			// it, which reproduces the original rejection and asks for
			// another repair — this is the same bounded loop, not a new one.
			slog.Warn("repair produced an invalid proposal, discarding and retrying if budget remains",
				"tenant_id", in.TenantID, "theme_slug", in.ThemeSlug, "attempt", attempt, "error", err)
			if attempt >= maxThemeCheckRetries {
				return nil, nil, fmt.Errorf("invalid model proposal (retry %d): %w", attempt, err)
			}
			emitter.emit(ctx, EventTypeCheckFailed, map[string]any{
				"attempt": attempt, "message": "repair produced an invalid proposal: " + err.Error(),
			})
			turns = append(turns, ai.Turn{Role: "user", Content: fmt.Sprintf(
				"That reply wasn't valid: %s. Resubmit a corrected, complete proposal (not a diff), "+
					"following the earlier instructions exactly. Never invent a placeholder path or "+
					"partial content — if you don't have a complete, verified proposal ready, call "+
					"propose_changes with needs_clarification: true, files: [], and explain why instead "+
					"of guessing.", err)})
			continue
		}
		result = retried
	}
}

func splitFindings(findings []themecheck.Finding) (errorFindings, warningFindings []themecheck.Finding) {
	for _, f := range findings {
		if f.Severity == themecheck.SeverityError {
			errorFindings = append(errorFindings, f)
		} else {
			warningFindings = append(warningFindings, f)
		}
	}
	return errorFindings, warningFindings
}

func findingRules(findings []themecheck.Finding) []string {
	rules := make([]string, len(findings))
	for i, f := range findings {
		rules[i] = f.Rule
	}
	return rules
}

func summarizeFindings(findings []themecheck.Finding) string {
	parts := make([]string, len(findings))
	for i, f := range findings {
		if f.Path != "" {
			parts[i] = fmt.Sprintf("[%s] %s: %s", f.Rule, f.Path, f.Message)
		} else {
			parts[i] = fmt.Sprintf("[%s] %s", f.Rule, f.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// recapAssistantTurn replays a rejected proposal's file content back to the
// model as its own prior turn. Without this the model retrying would have
// no memory of what it just wrote: a rejected proposal is never written to
// disk or the chat_generated_files audit trail (Check runs before
// buildWritePlan), so buildEditingFilesContext's real-file grounding won't
// have it either.
func recapAssistantTurn(result *ai.Result) string {
	var b strings.Builder
	if result.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", result.Summary)
	}
	for _, f := range result.Files {
		fmt.Fprintf(&b, "### %s (%s)\n%s\n\n", f.Path, f.Action, f.Content)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = "(no files proposed)"
	}
	return out
}

// repairPrompt is the new user turn sent back to the model after a rejected
// proposal — every error finding, since those are what actually blocked the
// write (warnings are surfaced to the merchant, never fed back for a retry).
func repairPrompt(errorFindings []themecheck.Finding) string {
	var b strings.Builder
	b.WriteString("Your last proposal failed validation against the theme engine spec. Fix these specific problems " +
		"and resubmit the complete corrected set of files (not a diff):\n\n")
	for _, f := range errorFindings {
		if f.Path != "" {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Rule, f.Path, f.Message)
		} else {
			fmt.Fprintf(&b, "- [%s] %s\n", f.Rule, f.Message)
		}
	}
	// A rejection on an existing file is often a sign the resubmitted
	// content was reconstructed from memory rather than the real file —
	// e.g. dropping the mandatory layout-start/layout-end boilerplate when
	// regenerating a page you were only asked to make a small change to.
	// The tool loop is still available on this retry; use it.
	b.WriteString("\nIf you're unsure of a file's exact current content, call read_theme_file on it again " +
		"before resubmitting — don't reconstruct it from memory, that's how boilerplate like the layout " +
		"renders above gets silently dropped.")
	return b.String()
}

// appendWarningsNote appends a short, merchant-readable note listing any
// warning-severity findings to summary. Rides on the existing summary text
// rather than a new chat_messages column — see phase 1 wiring notes; phase
// 3's generation_events log is the intended home for this once it exists.
func appendWarningsNote(summary string, warnings []themecheck.Finding) string {
	if len(warnings) == 0 {
		return summary
	}
	var b strings.Builder
	b.WriteString(summary)
	fmt.Fprintf(&b, "\n\nNote: %d warning(s):", len(warnings))
	for _, f := range warnings {
		if f.Path != "" {
			fmt.Fprintf(&b, "\n- %s: %s", f.Path, f.Message)
		} else {
			fmt.Fprintf(&b, "\n- %s", f.Message)
		}
	}
	return b.String()
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

// validateProposal re-checks every path the model proposed against the same
// rules internal/ai's system prompt already asked it to follow — defense in
// depth against a model mistake, never trusting model output as
// automatically safe just because it was asked nicely. mode restricts what's
// allowed further — see validateBrandModeProposal.
func validateProposal(r *ai.Result, mode string) error {
	if mode == ai.GenerationModeBrand {
		return validateBrandModeProposal(r)
	}
	for _, f := range r.Files {
		// Not re-embedding f.Path here: themefs' error already includes a
		// bounded preview of it. A proposal gone badly wrong can put an
		// entire file's content where a path belongs, and doubling that
		// blob into an outer wrapper is exactly the duplication that made
		// an earlier version of this error unreadable (and huge) in the
		// chat UI.
		if err := themefs.ValidateGeneratedFilePath(f.Path); err != nil {
			return fmt.Errorf("proposed file rejected: %w", err)
		}
		if f.Action != "create" && f.Action != "update" {
			return fmt.Errorf("file %q: invalid action %q", f.Path, f.Action)
		}
	}
	for _, p := range r.LayoutLinksToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("proposed layout css link rejected: %w", err)
		}
	}
	for _, p := range r.LayoutScriptsToAdd {
		if err := themefs.ValidateGeneratedFilePath(p); err != nil {
			return fmt.Errorf("proposed layout js link rejected: %w", err)
		}
	}
	return nil
}

// validateBrandModeProposal enforces phase 7's brand-turn restriction: this
// turn may only ever update defaults.json — never a .liquid/.css/.js file,
// a page registration, or a layout link/script, all of which are
// structural decisions the brand turn was never asked to make.
func validateBrandModeProposal(r *ai.Result) error {
	for _, f := range r.Files {
		if f.Path != pathDefaultsJSON {
			return fmt.Errorf("brand mode may only propose %q, got %q", pathDefaultsJSON, f.Path)
		}
		if f.Action != "update" {
			return fmt.Errorf("brand mode: %q action must be \"update\", got %q", pathDefaultsJSON, f.Action)
		}
	}
	if r.PageRegistryEntry != nil {
		return fmt.Errorf("brand mode must not register a page")
	}
	if len(r.LayoutLinksToAdd) > 0 || len(r.LayoutScriptsToAdd) > 0 {
		return fmt.Errorf("brand mode must not register layout links/scripts")
	}
	return nil
}

// writtenFile is one proposed file after it's been written to disk, paired
// with whatever content it replaced — captured before the write so the
// audit row persisted afterward (see persistFileRecords) can still record
// what changed, even though the real "before" state is gone from disk by then.
type writtenFile struct {
	generated ai.GeneratedFile
	previous  *string
}

// planFile is one file a writePlan will commit — either a proposed file
// (action/content straight from the model) or the recomputed content of a
// shared file (a layout file) after folding in this turn's splice. previous
// is only meaningful for proposed files (see writtenFile) and is nil
// otherwise. pageMeta is set only when this file is the page.liquid file
// PageRegistryEntry describes — flowpos-backend's own theme-file API upserts
// pages.json itself from these fields (see themefs.Store.WriteFile), so this
// service no longer computes pages.json content directly.
type planFile struct {
	path     string
	action   FileAction
	content  string
	previous *string
	pageMeta *themefs.PageMeta
}

// writePlan is everything one turn needs to commit to the real theme,
// computed entirely in memory before anything is written — see
// buildWritePlan/commitWritePlan.
type writePlan struct {
	files       []planFile
	layoutStart *planFile
	layoutEnd   *planFile
}

// paths lists every path this plan will write, in the same order it's
// computed — used only for EventTypeStaged's narration payload; the write
// itself (commitWritePlan) doesn't need this, it walks the struct directly.
func (p writePlan) paths() []string {
	paths := make([]string, 0, len(p.files)+2)
	for _, f := range p.files {
		paths = append(paths, f.path)
	}
	for _, f := range []*planFile{p.layoutStart, p.layoutEnd} {
		if f != nil {
			paths = append(paths, f.path)
		}
	}
	return paths
}

// buildWritePlan computes every file this turn would write — proposed
// files verbatim (with page metadata attached to the one matching
// PageRegistryEntry, if any), plus the layout-file splices — using only
// reads, never a write. Nothing is committed until commitWritePlan runs, so
// a failure here (a layout file missing its insertion marker, a page
// registry entry with no matching file) leaves the real theme completely
// untouched instead of partially, silently modified.
func (s *Service) buildWritePlan(ctx context.Context, storeAuth themefs.RequestAuth, result *ai.Result) (writePlan, error) {
	var plan writePlan

	for _, f := range result.Files {
		previous, err := s.store.ReadFile(ctx, storeAuth, f.Path)
		if err != nil {
			return writePlan{}, fmt.Errorf("read %q: %w", f.Path, err)
		}
		var previousPtr *string
		if previous != "" {
			previousPtr = &previous
		}
		plan.files = append(plan.files, planFile{
			path:     f.Path,
			action:   FileAction(f.Action),
			content:  f.Content,
			previous: previousPtr,
		})
	}

	if result.PageRegistryEntry != nil {
		entry := result.PageRegistryEntry
		// entry.Path is the route prefix ("/pages" or "/pages/auth"), never a
		// file path — the proposed file's actual theme-relative path has to be
		// derived from it the same way §5 requires: page == the .liquid file's
		// basename.
		wantPath := "pages/" + entry.Page + ".liquid"
		if entry.Path == "/pages/auth" {
			wantPath = "pages/auth/" + entry.Page + ".liquid"
		}
		matched := false
		for i := range plan.files {
			if plan.files[i].path != wantPath {
				continue
			}
			plan.files[i].pageMeta = &themefs.PageMeta{
				Title:          entry.Title,
				Slug:           entry.Slug,
				Type:           entry.Type,
				Status:         entry.Status,
				SEOTitle:       entry.SEOTitle,
				SEODescription: entry.SEODescription,
				SEOKeywords:    entry.SEOKeywords,
				OGTitle:        entry.OGTitle,
				OGDescription:  entry.OGDescription,
				OGImagePath:    entry.OGImagePath,
			}
			matched = true
			break
		}
		if !matched {
			return writePlan{}, fmt.Errorf("register page: page_registry_entry (page %q, path %q) has no matching proposed file at %q", entry.Page, entry.Path, wantPath)
		}
	}

	if len(result.LayoutLinksToAdd) > 0 {
		current, err := s.store.ReadFile(ctx, storeAuth, pathLayoutStart)
		if err != nil {
			return writePlan{}, fmt.Errorf("add layout css links: %w", err)
		}
		changedAny := false
		for _, path := range result.LayoutLinksToAdd {
			updated, changed, err := themefs.AddStylesheetLink(current, path)
			if err != nil {
				return writePlan{}, fmt.Errorf("add layout css link %q: %w", path, err)
			}
			if changed {
				current = updated // so a second link in the same turn splices against the first
				changedAny = true
			}
		}
		if changedAny {
			plan.layoutStart = &planFile{path: pathLayoutStart, content: current}
		}
	}

	if len(result.LayoutScriptsToAdd) > 0 {
		current, err := s.store.ReadFile(ctx, storeAuth, pathLayoutEnd)
		if err != nil {
			return writePlan{}, fmt.Errorf("add layout js links: %w", err)
		}
		changedAny := false
		for _, path := range result.LayoutScriptsToAdd {
			updated, changed, err := themefs.AddDeferredScript(current, path)
			if err != nil {
				return writePlan{}, fmt.Errorf("add layout js link %q: %w", path, err)
			}
			if changed {
				current = updated
				changedAny = true
			}
		}
		if changedAny {
			plan.layoutEnd = &planFile{path: pathLayoutEnd, content: current}
		}
	}

	return plan, nil
}

// commitWritePlan writes everything in plan through flowpos-backend's own
// theme-file API (each individual write already atomic on its side — see
// themefs.Store.WriteFile). Only the proposed files (plan.files) get an
// audit trail (see persistFileRecords) — the layout files are shared,
// structurally-spliced config, not "generated files" in their own right.
func (s *Service) commitWritePlan(ctx context.Context, storeAuth themefs.RequestAuth, plan writePlan) ([]writtenFile, error) {
	written := make([]writtenFile, 0, len(plan.files))
	for _, f := range plan.files {
		if err := s.store.WriteFile(ctx, storeAuth, f.path, f.content, f.pageMeta); err != nil {
			return written, fmt.Errorf("write %q: %w", f.path, err)
		}
		written = append(written, writtenFile{
			generated: ai.GeneratedFile{Path: f.path, Action: string(f.action), Content: f.content},
			previous:  f.previous,
		})
	}
	for _, f := range []*planFile{plan.layoutStart, plan.layoutEnd} {
		if f == nil {
			continue
		}
		if err := s.store.WriteFile(ctx, storeAuth, f.path, f.content, nil); err != nil {
			return written, fmt.Errorf("write %q: %w", f.path, err)
		}
	}
	return written, nil
}

// persistFileRecords writes the audit row for each file already written to
// disk (see commitWritePlan) — done after the assistant message exists
// since chat_generated_files.message_id is a foreign key into it.
func (s *Service) persistFileRecords(ctx context.Context, c chat.Chat, messageID string, written []writtenFile) ([]GeneratedFile, error) {
	files := make([]GeneratedFile, 0, len(written))
	now := time.Now().UTC()
	for _, w := range written {
		f := GeneratedFile{
			ID:              uuid.NewString(),
			MessageID:       messageID,
			ChatID:          c.ID,
			FilePath:        w.generated.Path,
			Action:          FileAction(w.generated.Action),
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

// FilesForChat returns every generated file ever written across a chat's
// whole history — used to hydrate GET /chat so reopening the page still
// shows each turn's "Generated files" card, not just the most recent one.
// Does not check ownership itself; the caller (the chat handler) has
// already scoped the chat to the requesting tenant.
// LoadThemeFiles fetches every .liquid template's content in the active
// theme, keyed by theme-relative path — used by the render preview (phase
// 5), which needs the whole theme available so {% render %} can resolve
// any component/partial the previewed page composes, not just the one
// entry file. CSS/JS/JSON/image paths are skipped: nothing but a template
// is ever a render target.
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

func (s *Service) LoadThemeFiles(ctx context.Context, storeAuth themefs.RequestAuth) (map[string]string, error) {
	tree, err := s.store.ListFiles(ctx, storeAuth)
	if err != nil {
		return nil, fmt.Errorf("list theme files: %w", err)
	}
	paths := make(map[string]bool)
	flattenFileTree(tree, paths)

	files := make(map[string]string, len(paths))
	for path := range paths {
		if !strings.HasSuffix(path, ".liquid") {
			continue
		}
		content, err := s.store.ReadFile(ctx, storeAuth, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		files[path] = content
	}
	return files, nil
}

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

// keyedMutex hands out one mutex per key, created lazily — same shape as
// ratelimit.PerTenantLimiter, minus the token-bucket part. The map grows by
// one entry per distinct theme_slug ever seen by this process, not per
// request — bounded by how many themes actually exist, unlike a per-token
// cache, so no sweep/eviction is needed here.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[string]*sync.Mutex)}
}

// Lock blocks until key's lock is held, and returns the func to release it.
func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	lock, ok := k.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		k.locks[key] = lock
	}
	k.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}
