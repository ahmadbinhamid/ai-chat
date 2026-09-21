package ai

import (
	"github.com/anthropics/anthropic-sdk-go"
)

// usageTokenAttrs returns slog attrs for usage fields that the provider
// actually reported. Optional fields omitted from the response are marked
// *_available=false rather than logged as a fake zero.
func usageTokenAttrs(usage anthropic.Usage) []any {
	attrs := []any{
		"input_tokens", usage.InputTokens,
		"output_tokens", usage.OutputTokens,
	}
	reasoning := usage.OutputTokensDetails.ThinkingTokens
	reasoningOK := usage.OutputTokensDetails.JSON.ThinkingTokens.Valid()
	if reasoningOK {
		attrs = append(attrs, "reasoning_tokens", reasoning, "reasoning_tokens_available", true)
	} else {
		attrs = append(attrs, "reasoning_tokens_available", false)
	}
	if usage.JSON.CacheReadInputTokens.Valid() {
		attrs = append(attrs, "cache_read_input_tokens", usage.CacheReadInputTokens, "cache_read_input_tokens_available", true)
	} else {
		attrs = append(attrs, "cache_read_input_tokens_available", false)
	}
	if usage.JSON.CacheCreationInputTokens.Valid() {
		attrs = append(attrs, "cache_creation_input_tokens", usage.CacheCreationInputTokens, "cache_creation_input_tokens_available", true)
	} else {
		attrs = append(attrs, "cache_creation_input_tokens_available", false)
	}
	return attrs
}

// usageTokenParts extracts numeric token fields for TurnMetrics folding.
func usageTokenParts(usage anthropic.Usage) (input, output, reasoning int64, reasoningOK bool, cacheRead, cacheCreate int64, cacheReadOK, cacheCreateOK bool) {
	input = usage.InputTokens
	output = usage.OutputTokens
	reasoning = usage.OutputTokensDetails.ThinkingTokens
	reasoningOK = usage.OutputTokensDetails.JSON.ThinkingTokens.Valid()
	if usage.JSON.CacheReadInputTokens.Valid() {
		cacheRead = usage.CacheReadInputTokens
		cacheReadOK = true
	}
	if usage.JSON.CacheCreationInputTokens.Valid() {
		cacheCreate = usage.CacheCreationInputTokens
		cacheCreateOK = true
	}
	return
}
