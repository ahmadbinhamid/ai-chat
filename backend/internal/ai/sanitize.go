package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared"
)

// genericGenerationError is shown whenever the underlying error doesn't
// match a recognized, safe-to-summarize category below.
const genericGenerationError = "something went wrong while generating a response — please try again in a moment"

// ExhaustedSearchReply: running out of rounds means the cause wasn't found, not that the request was too big.
const ExhaustedSearchReply = "I looked into this but couldn't find the cause. Could you tell me exactly what happens when you try it?"

const stuckInTextMessage = "I wasn't able to work out how to do that — could you rephrase it, or add a bit more detail about what you'd like to change?"

// ErrDraftReversionUnrepaired: every repair still dropped the merchant's earlier unsaved work, so nothing was staged.
var ErrDraftReversionUnrepaired = errors.New("proposal kept undoing earlier unsaved changes")

const draftReversionUnrepairedMessage = "I couldn't make this change without undoing your earlier unsaved changes. " +
	"Apply or discard them first, or tell me to undo them."

// SanitizeError turns any error into a short, vendor-neutral message safe to show a merchant.
// Callers should still log the original server-side.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	// Plain answers to the merchant, not errors, so they carry no error prefix.
	if msg, ok := honestMessage(err); ok {
		return msg
	}
	return fmt.Sprintf("Error from AI agent: %s", categorizeError(err))
}

func honestMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, errStuckInTextReplies):
		return stuckInTextMessage, true
	case errors.Is(err, ErrDraftReversionUnrepaired):
		return draftReversionUnrepairedMessage, true
	case strings.Contains(strings.ToLower(err.Error()), "did not call propose_changes within"):
		return ExhaustedSearchReply, true
	}
	return "", false
}

// categorizeError maps err to a short, actionable, provider-neutral reason. A typed
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
			// 521-524: Cloudflare's own origin-unreachable/timeout codes, seen
			// when buildSnapshot hits a momentarily-down origin, not the AI provider.
			return "temporarily unavailable — please try again shortly"
		}
	}

	if errors.Is(err, errMaxTokensTruncated) {
		return "the response was too large to complete — please try a smaller request"
	}

	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "the request timed out — please try again"
	}

	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "accumulate stream") || strings.Contains(lower, "error converting content block to json"):
		// A dropped connection or truncated response cut the stream off mid-chunk
		// before the SDK could reassemble valid JSON.
		return "the connection to the AI provider was interrupted mid-response — please try again"
	case strings.Contains(lower, "credit balance") || strings.Contains(lower, "insufficient balance") ||
		strings.Contains(lower, "payment required") || (strings.Contains(lower, "insufficient") && strings.Contains(lower, "credit")):
		// "insufficient balance"/"payment required" cover DeepSeek's own wording
		// for this (its 402 body has no "credit" in it at all).
		return "the account is out of credits — please contact support"
	case strings.Contains(lower, "didn't pass validation after"):
		return "the generated changes couldn't be validated after multiple attempts — please try a smaller or more specific request"
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
