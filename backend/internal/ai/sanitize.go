package ai

import (
	"fmt"
	"strings"
)

// genericGenerationError is shown whenever the underlying error doesn't
// match a recognized, safe-to-summarize category below.
const genericGenerationError = "something went wrong while generating a response — please try again in a moment"

// SanitizeError turns any error from Generate/Summarize into a short,
// vendor-neutral message safe to show a merchant or store as chat history.
// The raw error can contain the backing AI provider's name (config.AIProvider
// is an internal implementation detail, never something a merchant should
// see referenced), request IDs, and a raw JSON error body — none of that is
// merchant-appropriate regardless of which provider is configured. Callers
// should still log the original error server-side (slog.Error et al.) for
// debugging; this is only for anything a merchant can see.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("Error from AI agent: %s", categorizeError(err.Error()))
}

// categorizeError maps common, recognizable failure text to a short,
// actionable, provider-neutral reason. Order matters: more specific matches
// (e.g. "truncated at the max_tokens limit") are checked before broader ones
// that could otherwise shadow them.
func categorizeError(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "credit balance") || (strings.Contains(lower, "insufficient") && strings.Contains(lower, "credit")):
		return "the account is out of credits — please contact support"
	case strings.Contains(lower, "truncated at the max_tokens limit"):
		return "the response was too large to complete — please try a smaller request"
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "429"):
		return "too many requests right now — please try again shortly"
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return "the request timed out — please try again"
	case strings.Contains(lower, "overloaded") || strings.Contains(lower, "502") || strings.Contains(lower, "503"):
		return "temporarily unavailable — please try again shortly"
	default:
		return genericGenerationError
	}
}
