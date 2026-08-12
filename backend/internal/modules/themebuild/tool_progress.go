package themebuild

import (
	"context"
	"encoding/json"
	"strings"

	"ai-chat/internal/ai"
)

// maxToolCallDetailChars bounds a tool_call event's path/pattern field.
// path is already bounded by real filesystem paths in practice, but
// pattern is model-generated text landing in a payload the merchant's
// browser renders (see execGrepTheme) with no other size limit upstream —
// truncated here regardless of source, since a runaway path is no more
// welcome in a persisted payload than a runaway pattern.
const maxToolCallDetailChars = 100

// toolProgressEmitter adapts *eventEmitter to ai.ToolProgress — the bridge
// that turns internal/ai's dependency-free tool-loop callbacks (see
// ai.ToolProgress's doc comment for why that interface itself knows
// nothing about events, a bus, or WebSockets) into durable stream events a
// merchant watches live, Cursor/Claude-Code style. ctx is captured once at
// construction (there's nowhere for ToolStarted/ToolFinished to take one —
// see ai.ToolProgress's signature) rather than threaded through every
// call — the same ctx already used for every other emitter.emit call in
// the same doGenerate/generateValidProposal/checkAndRepair invocation this
// adapter is built for.
type toolProgressEmitter struct {
	ctx     context.Context
	emitter *eventEmitter
}

// ToolStarted emits EventTypeToolCall with a payload a merchant can read as
// narration — "tool": the raw name plus, for the two tools whose input
// says something a human cares about, "path" (read_theme_file) or
// "pattern" (grep_theme). list_theme_files takes no interesting input, so
// its payload is just {"tool": "list_theme_files"}. Unmarshal failures are
// silently ignored (payload just falls back to {"tool": name}) — a
// malformed input here would already be about to fail toolExec itself
// moments later with its own, better-contextualized error; this narration
// path isn't the place to surface that.
func (p toolProgressEmitter) ToolStarted(name string, input json.RawMessage) {
	payload := map[string]any{"tool": name}
	switch name {
	case "read_theme_file":
		var args readThemeFileInput
		if json.Unmarshal(input, &args) == nil && len(args.Paths) > 0 {
			// The model can ask for up to maxToolReadPaths files in one
			// call; the step list narrates the first, which is what a
			// merchant watching actually cares about — a multi-file read
			// still reads as one step ("Reading pages/home.liquid"), not a
			// wall of near-identical ones.
			payload["path"] = truncateForDisplay(args.Paths[0], maxToolCallDetailChars)
		}
	case "grep_theme":
		var args grepThemeInput
		if json.Unmarshal(input, &args) == nil && args.Pattern != "" {
			// Model-generated, unsanitized text landing in a persisted
			// payload the merchant's browser renders — truncated
			// server-side as defense in depth; the frontend is separately
			// required to render this as text, never HTML.
			payload["pattern"] = truncateForDisplay(args.Pattern, maxToolCallDetailChars)
		}
	}
	p.emitter.emit(p.ctx, EventTypeToolCall, payload)
}

// ToolFinished emits EventTypeToolResult with the summary Generate already
// computed (see ai.summarizeToolResult) — always, even when err != nil (a
// failed call is itself an outcome the step list should show, not a gap in
// it). err itself isn't part of the payload: summary already encodes
// failure ("failed: ..." — see summarizeToolResult), so this would just be
// the same information twice.
func (p toolProgressEmitter) ToolFinished(_ string, summary string, _ error) {
	p.emitter.emit(p.ctx, EventTypeToolResult, map[string]string{"summary": summary})
}

// onThinkingDelta builds the onDelta callback passed to ai.Generate — routes
// each already-coalesced chunk (see ai.deltaCoalescer) to emitLive, never
// emit: this is exactly the ephemeral, high-frequency case emitLive exists
// for (see its doc comment and EventTypeThinking's) — durably storing every
// streamed chunk of every generation, and burning a seq number per chunk,
// is the one mistake this whole feature is built to avoid.
func onThinkingDelta(ctx context.Context, emitter *eventEmitter) func(string) {
	return func(text string) {
		emitter.emitLive(ctx, EventTypeThinking, map[string]string{"text": text})
	}
}

// toolProgressFor builds a toolProgressEmitter for one Generate call —
// named rather than a bare struct literal at each of the two call sites so
// a future third call site (there have been two for a while — the initial
// attempt and checkAndRepair's retries) picks this up automatically.
func toolProgressFor(ctx context.Context, emitter *eventEmitter) ai.ToolProgress {
	return toolProgressEmitter{ctx: ctx, emitter: emitter}
}

// truncateForDisplay bounds s to max runes (not bytes — a path or pattern
// can contain multi-byte characters), collapsing newlines to spaces first
// since this is meant to render as one display line.
func truncateForDisplay(s string, max int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}
