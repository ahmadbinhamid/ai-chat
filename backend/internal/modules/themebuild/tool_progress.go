package themebuild

import (
	"context"
	"encoding/json"
	"strings"

	"ai-chat/internal/ai"
)

// maxToolCallDetailChars bounds a tool_call event's path/pattern field; pattern is
// model-generated text with no other size limit upstream.
const maxToolCallDetailChars = 100

// toolProgressEmitter adapts *eventEmitter to ai.ToolProgress. ctx is captured at construction
// since ToolStarted/ToolFinished have no ctx parameter of their own.
type toolProgressEmitter struct {
	ctx     context.Context
	emitter *eventEmitter
}

// ToolStarted emits EventTypeToolCall with the tool name plus "path"/"pattern" when worth
// narrating. Unmarshal failures fall back to {"tool": name}; toolExec surfaces the error itself.
func (p toolProgressEmitter) ToolStarted(name string, input json.RawMessage) {
	payload := map[string]any{"tool": name}
	switch name {
	case "read_theme_file":
		var args readThemeFileInput
		if json.Unmarshal(input, &args) == nil && len(args.Paths) > 0 {
			// Narrate only the first path of a multi-file read, so it reads as one step, not a wall of them.
			payload["path"] = truncateForDisplay(args.Paths[0], maxToolCallDetailChars)
		}
	case "grep_theme":
		var args grepThemeInput
		if json.Unmarshal(input, &args) == nil && args.Pattern != "" {
			// Truncated server-side as defense in depth; frontend must render this as text, never HTML.
			payload["pattern"] = truncateForDisplay(args.Pattern, maxToolCallDetailChars)
		}
	}
	p.emitter.emit(p.ctx, EventTypeToolCall, payload)
}

// ToolFinished emits EventTypeToolResult with summary always, even on error — a failed call
// is still an outcome to show. err is omitted since summary already encodes failure text.
func (p toolProgressEmitter) ToolFinished(_ string, summary string, _ error) {
	p.emitter.emit(p.ctx, EventTypeToolResult, map[string]string{"summary": summary})
}

// onThinkingDelta routes each coalesced chunk to emitLive, never emit — durably storing every
// streamed chunk (and burning a seq number per chunk) is the mistake this avoids.
func onThinkingDelta(ctx context.Context, emitter *eventEmitter) func(string) {
	return func(text string) {
		emitter.emitLive(ctx, EventTypeThinking, map[string]string{"text": text})
	}
}

// toolProgressFor builds a toolProgressEmitter for one Generate call.
func toolProgressFor(ctx context.Context, emitter *eventEmitter) ai.ToolProgress {
	return toolProgressEmitter{ctx: ctx, emitter: emitter}
}

// truncateForDisplay bounds s to max runes (not bytes), collapsing newlines to spaces first.
func truncateForDisplay(s string, max int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}
