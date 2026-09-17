package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// defaultStreamIdleTimeout is how long a single provider stream may sit
// without yielding the next SSE event before we cancel it. Measured local
// stall (DeepSeek truncated after ~167s with no useful completion) made the
// 65-minute generation timeout useless for interactive edits — idle detection
// is independent of that budget. Override via Generator.SetStreamIdleTimeout /
// AI_STREAM_IDLE_TIMEOUT_MS.
const defaultStreamIdleTimeout = 12 * time.Second

// defaultStreamFirstTokenTimeout bounds how long one NewStreaming attempt may
// wait for the first narration/thinking content token after the stream opens.
// Idle timeout alone resets on every SSE control frame (message_start, etc.),
// so a connection that dribbles non-content events would never trip idle —
// this deadline covers the pre-first-token window explicitly.
// Override via Generator.SetStreamFirstTokenTimeout /
// AI_STREAM_FIRST_TOKEN_TIMEOUT_MS. Kept longer than idle to avoid aggressive
// false cancellations on slow-but-healthy TTFT.
const defaultStreamFirstTokenTimeout = 45 * time.Second

// streamAccumulateMaxAttempts bounds retries for truncated/idle provider
// streams within one tool-loop iteration. Interactive edits allow one
// controlled retry (2 attempts total) — the pre-fix policy of 3× with a 5s
// delay let a 167s stall + delay dominate wall clock.
const streamAccumulateMaxAttempts = 2

// streamAccumulateRetryDelay is the pause between a truncated/idle stream
// and its one allowed retry — short on purpose (interactive latency).
const streamAccumulateRetryDelay = 1 * time.Second

// errStreamIdleTimeout is returned when the provider yields no SSE event for
// streamIdleTimeout. Retryable via isRetryableStreamErr.
var errStreamIdleTimeout = errors.New("provider stream idle timeout: no progress")

// errStreamFirstTokenTimeout is returned when no content token arrives within
// streamFirstTokenTimeout. Retryable like idle — same controlled budget.
var errStreamFirstTokenTimeout = errors.New("provider stream first-token timeout: no content")

// errStreamTruncated wraps Accumulate / incomplete JSON failures so callers
// and logs can classify them without string-matching alone.
var errStreamTruncated = errors.New("provider stream truncated or incomplete JSON")

// streamAttemptMeta is per-attempt telemetry for one NewStreaming call.
type streamAttemptMeta struct {
	Attempt          int
	StreamStart      time.Time
	FirstDeltaAt     time.Time
	LastProgressAt   time.Time
	StreamEnd        time.Time
	IdleTimeout      bool
	FirstTokenTimeout bool
	Truncated        bool
	Retried          bool
	RetryDecision    string
	TTFTAttemptMs    int64
	StreamDurationMs int64
	IdleWaitMs       int64 // time spent waiting for the Next() that timed out, if any
	ErrorType        string
}

// isRetryableStreamErr reports whether a stream attempt failure should get
// the single controlled retry (idle timeout or truncated/garbled accumulate).
func isRetryableStreamErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errStreamIdleTimeout) ||
		errors.Is(err, errStreamFirstTokenTimeout) ||
		errors.Is(err, errStreamTruncated) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "accumulate stream") ||
		strings.Contains(msg, "unexpected end of JSON input") ||
		strings.Contains(msg, "error converting content block to JSON")
}

// classifyStreamErr returns a short error_type tag for structured logs.
func classifyStreamErr(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errStreamIdleTimeout):
		return "stream_timeout"
	case errors.Is(err, errStreamFirstTokenTimeout):
		return "first_token_timeout"
	case errors.Is(err, errStreamTruncated),
		strings.Contains(err.Error(), "accumulate stream"),
		strings.Contains(err.Error(), "unexpected end of JSON input"):
		return "stream_truncated"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "stream_error"
	}
}

// wrapAccumulateErr marks incomplete/garbled Accumulate failures as
// errStreamTruncated while preserving the original text for diagnostics.
func wrapAccumulateErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: accumulate stream: %v", errStreamTruncated, err)
}

// streamIdleTimeout resolves the Generator's configured idle budget.
func (g *Generator) streamIdleTimeout() time.Duration {
	if g != nil && g.idleTimeout > 0 {
		return g.idleTimeout
	}
	return defaultStreamIdleTimeout
}

// SetStreamIdleTimeout configures how long NewStreaming may block without an
// SSE event before the attempt is cancelled. Zero or negative keeps the
// default. Safe to call once at process startup.
func (g *Generator) SetStreamIdleTimeout(d time.Duration) {
	if g == nil {
		return
	}
	if d <= 0 {
		g.idleTimeout = defaultStreamIdleTimeout
		return
	}
	g.idleTimeout = d
}

// streamFirstTokenTimeout resolves the Generator's configured first-token budget.
func (g *Generator) streamFirstTokenTimeout() time.Duration {
	if g != nil && g.firstTokenTimeout > 0 {
		return g.firstTokenTimeout
	}
	return defaultStreamFirstTokenTimeout
}

// SetStreamFirstTokenTimeout configures how long NewStreaming may wait for
// the first narration/thinking content token. Zero or negative keeps the
// default. Independent of idle timeout (which resets on every SSE frame).
func (g *Generator) SetStreamFirstTokenTimeout(d time.Duration) {
	if g == nil {
		return
	}
	if d <= 0 {
		g.firstTokenTimeout = defaultStreamFirstTokenTimeout
		return
	}
	g.firstTokenTimeout = d
}

// defaultPreparedFirstTokenTimeout bounds TTFT for PageCreatePrepared calls —
// DeepSeek can exceed 25s even with thinking disabled; 40s cuts false
// timeouts without waiting the full interactive 45s budget twice.
const defaultPreparedFirstTokenTimeout = 60 * time.Second

// PreparedFirstTokenTimeout is the TTFT budget for PageCreatePrepared
// generations (exported for themebuild wiring / tests).
func PreparedFirstTokenTimeout() time.Duration {
	return defaultPreparedFirstTokenTimeout
}

// consumeProviderStream runs one NewStreaming attempt with an idle deadline
// independent of the parent generation timeout. Every successful Next()
// resets the idle timer (healthy long streams that keep emitting are fine).
// onDelta receives coalesced narration text the same way the previous inline
// loop did. firstTokenOverride, when > 0, replaces streamFirstTokenTimeout.
func (g *Generator) consumeProviderStream(
	ctx context.Context,
	params anthropic.MessageNewParams,
	onDelta func(string),
	attempt int,
	iteration int,
	generateStart time.Time,
	firstTokenLogged *bool,
	ttftMs *int64,
	firstTokenOverride time.Duration,
	progress ToolProgress,
) (anthropic.Message, streamAttemptMeta, error) {
	meta := streamAttemptMeta{
		Attempt:     attempt,
		StreamStart: time.Now(),
	}
	meta.LastProgressAt = meta.StreamStart

	idle := g.streamIdleTimeout()
	firstTokenDeadline := g.streamFirstTokenTimeout()
	if firstTokenOverride > 0 {
		firstTokenDeadline = firstTokenOverride
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream := g.client.Messages.NewStreaming(streamCtx, params)
	message := anthropic.Message{}
	emitted := 0
	coalescer := newDeltaCoalescer(onDelta)
	slowNotified := false

	defer func() {
		meta.StreamEnd = time.Now()
		meta.StreamDurationMs = meta.StreamEnd.Sub(meta.StreamStart).Milliseconds()
		if !meta.FirstDeltaAt.IsZero() {
			meta.TTFTAttemptMs = meta.FirstDeltaAt.Sub(meta.StreamStart).Milliseconds()
		}
		coalescer.flush()
	}()

	for {
		waitBudget := idle
		if meta.FirstDeltaAt.IsZero() {
			elapsed := time.Since(meta.StreamStart)
			remaining := firstTokenDeadline - elapsed
			if !slowNotified && elapsed >= firstTokenDeadline/2 {
				slowNotified = true
				if sp, ok := progress.(StreamStatusProgress); ok {
					sp.AITakingLonger(iteration, attempt)
				}
			}
			if remaining <= 0 {
				meta.FirstTokenTimeout = true
				meta.IdleWaitMs = elapsed.Milliseconds()
				meta.ErrorType = classifyStreamErr(errStreamFirstTokenTimeout)
				cancel()
				slog.Warn("ai: provider stream first-token timeout",
					"provider", g.provider,
					"model", g.modelName,
					"iteration", iteration,
					"attempt", attempt,
					"first_token_timeout_ms", firstTokenDeadline.Milliseconds(),
					"stream_elapsed_ms", elapsed.Milliseconds())
				return message, meta, errStreamFirstTokenTimeout
			}
			// Cap Next() wait so first-token deadline can fire even while
			// blocked on the next SSE frame (idle alone would wait the full
			// idle window first).
			if remaining < waitBudget {
				waitBudget = remaining
			}
		}

		ok, waitMs, err := nextWithIdle(streamCtx, cancel, stream, waitBudget)
		if err != nil {
			if errors.Is(err, errStreamIdleTimeout) && meta.FirstDeltaAt.IsZero() &&
				time.Since(meta.StreamStart) >= firstTokenDeadline {
				meta.FirstTokenTimeout = true
				meta.IdleWaitMs = time.Since(meta.StreamStart).Milliseconds()
				meta.ErrorType = classifyStreamErr(errStreamFirstTokenTimeout)
				return message, meta, errStreamFirstTokenTimeout
			}
			meta.IdleTimeout = errors.Is(err, errStreamIdleTimeout)
			meta.IdleWaitMs = waitMs
			meta.ErrorType = classifyStreamErr(err)
			return message, meta, err
		}
		if !ok {
			if err := stream.Err(); err != nil {
				meta.ErrorType = classifyStreamErr(err)
				return message, meta, fmt.Errorf("claude stream: %w", err)
			}
			return message, meta, nil
		}

		meta.LastProgressAt = time.Now()
		if err := message.Accumulate(stream.Current()); err != nil {
			wrapped := wrapAccumulateErr(err)
			meta.Truncated = true
			meta.ErrorType = classifyStreamErr(wrapped)
			slog.Warn("ai: provider stream truncated during accumulate",
				"provider", g.provider,
				"model", g.modelName,
				"iteration", iteration,
				"attempt", attempt,
				"stream_elapsed_ms", time.Since(meta.StreamStart).Milliseconds(),
				"error_type", meta.ErrorType,
				"error", err.Error())
			cancel()
			return message, meta, wrapped
		}

		if full := currentText(message); len(full) > emitted {
			now := time.Now()
			if meta.FirstDeltaAt.IsZero() {
				meta.FirstDeltaAt = now
				meta.TTFTAttemptMs = now.Sub(meta.StreamStart).Milliseconds()
			}
			if firstTokenLogged != nil && !*firstTokenLogged && len(full) > 0 {
				*firstTokenLogged = true
				if ttftMs != nil {
					*ttftMs = now.Sub(generateStart).Milliseconds()
				}
			}
			chunk := full[emitted:]
			emitted = len(full)
			coalescer.add(chunk)
		}
	}
}

// nextWithIdle waits for stream.Next() with an idle deadline. On idle it
// cancels streamCtx so the blocked Next() unblocks promptly. Every returned
// event resets the caller's idle window (caller loops and calls again).
func nextWithIdle(
	streamCtx context.Context,
	cancel context.CancelFunc,
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion],
	idle time.Duration,
) (ok bool, waitMs int64, err error) {
	type outcome struct {
		ok bool
	}
	ch := make(chan outcome, 1)
	startWait := time.Now()
	go func() {
		ch <- outcome{ok: stream.Next()}
	}()

	timer := time.NewTimer(idle)
	defer timer.Stop()

	select {
	case <-streamCtx.Done():
		waitMs = time.Since(startWait).Milliseconds()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
		}
		return false, waitMs, streamCtx.Err()
	case <-timer.C:
		waitMs = time.Since(startWait).Milliseconds()
		cancel() // unblock Next / abort HTTP body
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
		}
		return false, waitMs, errStreamIdleTimeout
	case o := <-ch:
		waitMs = time.Since(startWait).Milliseconds()
		return o.ok, waitMs, nil
	}
}

// logStreamAttempt emits the structured stream telemetry line required to
// prove idle/truncation/retry behaviour in production logs.
func logStreamAttempt(
	g *Generator,
	iteration int,
	meta streamAttemptMeta,
	msg anthropic.Message,
	err error,
	retryDecision string,
) {
	if g == nil {
		return
	}
	lastProgressMs := int64(0)
	if !meta.LastProgressAt.IsZero() {
		lastProgressMs = time.Since(meta.LastProgressAt).Milliseconds()
		if meta.StreamEnd.After(meta.LastProgressAt) {
			lastProgressMs = meta.StreamEnd.Sub(meta.LastProgressAt).Milliseconds()
		}
	}
	attrs := []any{
		"provider", g.provider,
		"model", g.modelName,
		"iteration", iteration,
		"attempt", meta.Attempt,
		"stream_start", meta.StreamStart.UTC().Format(time.RFC3339Nano),
		"stream_end", meta.StreamEnd.UTC().Format(time.RFC3339Nano),
		"stream_duration_ms", meta.StreamDurationMs,
		"ttft_attempt_ms", meta.TTFTAttemptMs,
		"last_progress_ms", lastProgressMs,
		"idle_wait_ms", meta.IdleWaitMs,
		"stream_timeout", meta.IdleTimeout,
		"first_token_timeout", meta.FirstTokenTimeout,
		"stream_truncated", meta.Truncated,
		"retry_decision", retryDecision,
		"error_type", meta.ErrorType,
		"input_tokens", msg.Usage.InputTokens,
		"output_tokens", msg.Usage.OutputTokens,
		"cache_read_input_tokens", msg.Usage.CacheReadInputTokens,
	}
	if !meta.FirstDeltaAt.IsZero() {
		attrs = append(attrs, "first_delta", meta.FirstDeltaAt.UTC().Format(time.RFC3339Nano))
	}
	if !meta.LastProgressAt.IsZero() {
		attrs = append(attrs, "last_delta", meta.LastProgressAt.UTC().Format(time.RFC3339Nano))
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
		slog.Warn("ai: provider stream attempt", attrs...)
		return
	}
	slog.Info("ai: provider stream attempt", attrs...)
}
