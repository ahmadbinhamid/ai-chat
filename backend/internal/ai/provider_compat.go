package ai

import (
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
)

// Provider capability helpers keep DeepSeek-specific wire behavior localized
// on *Generator rather than scattering `if provider == "deepseek"` through
// Generate. Anthropic remains the default Messages-API path.

// supportsPromptCacheControl reports whether the provider honors Anthropic
// cache_control breakpoints. DeepSeek's Anthropic-compat endpoint silently
// ignores them (confirmed at New()); we omit the field rather than send an
// unsupported Anthropic-only feature.
func (g *Generator) supportsPromptCacheControl() bool {
	if g == nil {
		return true
	}
	return g.provider != "deepseek"
}

// mustDisableThinking is true when this call must send thinking:{type:disabled}.
// DeepSeek V4 enables thinking by DEFAULT when the field is omitted — that
// alone caused long TTFT and 400s on named tool_choice. Prepared/simple-edit
// and themecheck repair turns disable thinking so forced propose_changes works
// and repair stays on the Phase 1 repair budget.
func (g *Generator) mustDisableThinking(tc ThemeContext) bool {
	if g == nil || g.provider != "deepseek" {
		return false
	}
	return tc.PageCreatePrepared || tc.SimpleEditOneShot || tc.Repair
}

// canForceNamedToolChoice reports whether tool_choice may name propose_changes.
// DeepSeek rejects/ignores named tool_choice while adaptive thinking is on;
// when thinking is disabled (mustDisableThinking), forcing works like Anthropic.
func (g *Generator) canForceNamedToolChoice(tc ThemeContext) bool {
	if g == nil || g.provider != "deepseek" {
		return true
	}
	return g.mustDisableThinking(tc)
}

// applyCacheControlEphemeral sets a 1h ephemeral cache breakpoint when the
// provider supports it; no-op for DeepSeek.
func (g *Generator) applyCacheControlEphemeral(block *anthropic.TextBlockParam) {
	if block == nil || !g.supportsPromptCacheControl() {
		return
	}
	cacheControl := anthropic.NewCacheControlEphemeralParam()
	cacheControl.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	block.CacheControl = cacheControl
}

// estimateToolsSchemaBytes returns a deterministic JSON size estimate of the
// tool definitions for metrics (never logged as content).
func estimateToolsSchemaBytes(tools []anthropic.ToolUnionParam) int {
	if len(tools) == 0 {
		return 0
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return len(b)
}

// estimateMessagesBytes sums text/image-bearing message content lengths for
// observability. Image base64 contributes its encoded length; no content is logged.
func estimateMessagesBytes(messages []anthropic.MessageParam) int {
	n := 0
	for _, m := range messages {
		for _, block := range m.Content {
			if block.OfText != nil {
				n += len(block.OfText.Text)
			}
			if block.OfImage != nil && block.OfImage.Source.OfBase64 != nil {
				n += len(block.OfImage.Source.OfBase64.Data)
			}
			if block.OfToolResult != nil {
				for _, c := range block.OfToolResult.Content {
					if c.OfText != nil {
						n += len(c.OfText.Text)
					}
				}
			}
			if block.OfToolUse != nil {
				n += len(block.OfToolUse.Name)
				switch v := block.OfToolUse.Input.(type) {
				case []byte:
					n += len(v)
				case string:
					n += len(v)
				case json.RawMessage:
					n += len(v)
				default:
					if b, err := json.Marshal(v); err == nil {
						n += len(b)
					}
				}
			}
		}
	}
	return n
}
