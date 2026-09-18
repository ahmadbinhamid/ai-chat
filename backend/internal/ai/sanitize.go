package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"ai-chat/internal/genfail"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared"
)

// genericGenerationError is shown whenever the underlying error doesn't
// match a recognized, safe-to-summarize category below.
const genericGenerationError = "something went wrong while generating a response — please try again in a moment"

// SanitizeError turns any error from Generate/Summarize into a short,
// vendor-neutral message safe to show a merchant or store as chat history.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	// Prefer genfail whenever Classify knows a specific code — compound
	// multi-page create used to fall through to the generic message.
	c := genfail.Classify(err)
	if c.Code != genfail.CodeUnknown && strings.TrimSpace(c.Message) != "" {
		return fmt.Sprintf("Error from AI agent: %s", c.Message)
	}
	return fmt.Sprintf("Error from AI agent: %s", categorizeError(err))
}

// FailureClassification returns structured failure metadata for events/logs.
func FailureClassification(err error) genfail.Classification {
	return genfail.Classify(err)
}

func categorizeError(err error) string {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Type() == shared.ErrorTypeBillingError:
			return "the account is out of credits — please contact support"
		case apiErr.Type() == shared.ErrorTypeRateLimitError || apiErr.StatusCode == http.StatusTooManyRequests:
			return "too many requests right now — please try again shortly"
		case apiErr.Type() == shared.ErrorTypeTimeoutError:
			return "the request timed out — please try again"
		case apiErr.Type() == shared.ErrorTypeOverloadedError || apiErr.StatusCode == http.StatusBadGateway ||
			apiErr.StatusCode == http.StatusServiceUnavailable || (apiErr.StatusCode >= 520 && apiErr.StatusCode <= 524):
			return "temporarily unavailable — please try again shortly"
		}
	}

	if errors.Is(err, ErrMaxTokensTruncated) {
		return "the response was too large to complete — please try a smaller request"
	}
	if errors.Is(err, errStreamFirstTokenTimeout) {
		return "the AI provider is taking longer than expected to start — please try again"
	}
	if errors.Is(err, errStreamIdleTimeout) {
		return "the AI response stalled mid-stream — please try again"
	}
	if errors.Is(err, errStreamTruncated) {
		return "the connection to the AI provider was interrupted mid-response — please try again"
	}

	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "the request timed out — please try again"
	}

	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "incomplete multi-page"):
		return "the AI did not create all requested pages and register them in Pages. Please try again."
	case strings.Contains(lower, "invalid model proposal"):
		return "the generated changes could not be validated. Please try again."
	case strings.Contains(lower, "accumulate stream") || strings.Contains(lower, "error converting content block to json") ||
		strings.Contains(lower, "provider stream truncated") || strings.Contains(lower, "provider stream idle"):
		return "the connection to the AI provider was interrupted mid-response — please try again"
	case strings.Contains(lower, "credit balance") || strings.Contains(lower, "insufficient balance") ||
		strings.Contains(lower, "payment required") || (strings.Contains(lower, "insufficient") && strings.Contains(lower, "credit")):
		return "the account is out of credits — please contact support"
	case strings.Contains(lower, "simple_edit:") || errors.Is(err, ErrSimpleEditBudget):
		return "the change needed a larger edit pass — please try again"
	case strings.Contains(lower, "did not call propose_changes within"):
		return "the change needed another pass and couldn't finish — please try again"
	case strings.Contains(lower, "didn't pass validation after"):
		return "the generated changes couldn't be validated after multiple attempts — please try again"
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "429"):
		return "too many requests right now — please try again shortly"
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return "the request timed out — please try again"
	case strings.Contains(lower, "overloaded") || strings.Contains(lower, "502") || strings.Contains(lower, "503") ||
		strings.Contains(lower, "521") || strings.Contains(lower, "522") || strings.Contains(lower, "523") || strings.Contains(lower, "524"):
		return "temporarily unavailable — please try again shortly"
	default:
		return genericGenerationError
	}
}
