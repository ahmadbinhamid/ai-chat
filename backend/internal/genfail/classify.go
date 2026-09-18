// Package genfail classifies generation failures into safe, bounded codes
// for logs, metrics, and merchant-facing messages. Pure — no I/O.
package genfail

import (
	"context"
	"errors"
	"strings"
)

// Code is a stable, non-sensitive failure identifier.
type Code string

const (
	CodeCancelled                     Code = "CANCELLED"
	CodeAIGenerationTimeout           Code = "AI_GENERATION_TIMEOUT"
	CodeGenerationTimeout                  = CodeAIGenerationTimeout // alias (older clients)
	CodeProviderTimeout               Code = "PROVIDER_TIMEOUT"
	CodeAIProviderFirstTokenTimeout   Code = "AI_PROVIDER_FIRST_TOKEN_TIMEOUT"
	CodeStreamFirstTokenTimeout            = CodeAIProviderFirstTokenTimeout // alias
	CodeStreamIdleTimeout             Code = "STREAM_IDLE_TIMEOUT"
	CodeStreamTruncated          Code = "STREAM_TRUNCATED"
	CodeProviderRateLimit        Code = "PROVIDER_RATE_LIMIT"
	CodeProviderError            Code = "PROVIDER_ERROR"
	CodeMaxTokensTruncated       Code = "MAX_TOKENS_TRUNCATED"
	CodeValidationFailed         Code = "VALIDATION_FAILED"
	CodeIncompleteMultiPage      Code = "INCOMPLETE_MULTI_PAGE"
	CodeIncompleteProposal       Code = "INCOMPLETE_PROPOSAL"
	CodeProposalContractMismatch Code = "PROPOSAL_CONTRACT_MISMATCH"
	CodeCompoundPartial          Code = "COMPOUND_PARTIAL"
	CodeToolThrash               Code = "TOOL_THRASH"
	CodeQueueFull                Code = "QUEUE_FULL"
	CodeSessionExpired           Code = "SESSION_EXPIRED"
	CodeUnknown                  Code = "UNKNOWN"
)

// Classification is the structured outcome of Classify.
type Classification struct {
	Code      Code
	Stage     string // proposal_validate | provider | repair | persist | cancel | unknown
	Retryable bool
	Message   string // merchant-safe, without "Error from AI agent:" prefix
}

// Classify maps a generation error to a safe code + merchant message.
func Classify(err error) Classification {
	if err == nil {
		return Classification{Code: CodeUnknown, Stage: "unknown", Message: ""}
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	// Parent generation wall budget (10 minutes) — including compound
	// messages that mention the timeout while preserving completed steps.
	if errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(lower, "timed out after 10 minutes") ||
		strings.Contains(lower, "ai_generation_timeout") {
		outMsg := "Generation timed out after 10 minutes. Please try again."
		if strings.Contains(lower, "succeeded") || strings.Contains(lower, "create page") ||
			strings.Contains(lower, "needs another attempt") {
			outMsg = firstSentence(msg)
		}
		return Classification{
			Code: CodeAIGenerationTimeout, Stage: "generation", Retryable: true,
			Message: outMsg,
		}
	}

	// Compound partial messages must win over Unwrap()'d cancel —
	// the merchant needs "page 1 kept, page 2 failed", not a bare cancel.
	if strings.Contains(lower, "compound partial") ||
		(strings.Contains(lower, "needs another attempt") && (strings.Contains(lower, "succeeded") || strings.Contains(lower, "create page"))) {
		return Classification{
			Code: CodeCompoundPartial, Stage: "compound", Retryable: true,
			Message: firstSentence(msg),
		}
	}
	if strings.Contains(lower, "incomplete atomic page create") {
		return Classification{
			Code: CodeIncompleteProposal, Stage: "proposal_validate", Retryable: true,
			Message: "The AI created a page file but did not register it in Pages. Please try again.",
		}
	}
	if strings.Contains(lower, "proposal/tool contract") ||
		strings.Contains(lower, "destructive pages.json") ||
		(strings.Contains(lower, "page_registry_entry only registers") && strings.Contains(lower, "pages.json")) {
		return Classification{
			Code: CodeProposalContractMismatch, Stage: "proposal_validate", Retryable: true,
			Message: "The AI used the wrong page-registration format for this step. Please try again.",
		}
	}

	if errors.Is(err, context.Canceled) {
		return Classification{
			Code: CodeCancelled, Stage: "cancel", Retryable: false,
			Message: "Generation was cancelled.",
		}
	}

	switch {
	case strings.Contains(lower, "incomplete multi-page"):
		// Prefer contract-mismatch wording when the error is specifically
		// registry-vs-pages.json (not a missing liquid create).
		if strings.Contains(lower, "page_registry_entry only registers") || strings.Contains(lower, "proposal/tool contract") {
			return Classification{
				Code: CodeProposalContractMismatch, Stage: "proposal_validate", Retryable: true,
				Message: "The AI used the wrong page-registration format for this step. Please try again.",
			}
		}
		return Classification{
			Code: CodeIncompleteMultiPage, Stage: "proposal_validate", Retryable: true,
			Message: "The AI did not create all requested pages and register them in Pages. Please try again.",
		}
	case strings.Contains(lower, "oversized or forbidden rewrite"):
		return Classification{
			Code: CodeIncompleteProposal, Stage: "proposal_validate", Retryable: true,
			Message: "The AI tried to rewrite an existing page instead of creating new pages. Please try again.",
		}
	case strings.Contains(lower, "must update pages/") && strings.Contains(lower, "named page"):
		return Classification{
			Code: CodeIncompleteProposal, Stage: "proposal_validate", Retryable: true,
			Message: "The AI missed the page that needed updating. Please try again.",
		}
	case strings.Contains(lower, "invalid model proposal"):
		return Classification{
			Code: CodeValidationFailed, Stage: "proposal_validate", Retryable: true,
			Message: "The generated changes could not be validated. Please try again.",
		}
	case strings.Contains(lower, "didn't pass validation after"):
		return Classification{
			Code: CodeValidationFailed, Stage: "repair", Retryable: true,
			Message: "The generated changes couldn't be validated after multiple attempts — please try again.",
		}
	case strings.Contains(lower, "did not call propose_changes"):
		return Classification{
			Code: CodeToolThrash, Stage: "provider", Retryable: true,
			Message: "The change needed another pass and couldn't finish — please try again.",
		}
	case strings.Contains(lower, "first-token timeout") || strings.Contains(lower, "first token timeout"):
		return Classification{
			Code: CodeAIProviderFirstTokenTimeout, Stage: "provider", Retryable: true,
			Message: "The AI provider is taking longer than expected to start. Your changes are still bounded by a 10-minute generation limit. Please try again if the provider does not respond.",
		}
	case strings.Contains(lower, "stream idle") || strings.Contains(lower, "provider stream idle"):
		return Classification{
			Code: CodeStreamIdleTimeout, Stage: "provider", Retryable: true,
			Message: "The AI response stalled mid-stream — please try again.",
		}
	case strings.Contains(lower, "stream truncated") || strings.Contains(lower, "accumulate stream"):
		return Classification{
			Code: CodeStreamTruncated, Stage: "provider", Retryable: true,
			Message: "The connection to the AI provider was interrupted mid-response — please try again.",
		}
	case strings.Contains(lower, "max_tokens") || strings.Contains(lower, "truncated at the max_tokens"):
		return Classification{
			Code: CodeMaxTokensTruncated, Stage: "provider", Retryable: true,
			Message: "The response was too large to complete — please try a smaller request.",
		}
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests"):
		return Classification{
			Code: CodeProviderRateLimit, Stage: "provider", Retryable: true,
			Message: "Too many requests right now — please try again shortly.",
		}
	case strings.Contains(lower, "queue") && strings.Contains(lower, "full"):
		return Classification{
			Code: CodeQueueFull, Stage: "queue", Retryable: true,
			Message: "Too many generations are queued — please wait and try again.",
		}
	case strings.Contains(lower, "session expired") || strings.Contains(lower, "sign in again"):
		return Classification{
			Code: CodeSessionExpired, Stage: "auth", Retryable: false,
			Message: "Your session expired — please sign in again and retry.",
		}
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return Classification{
			Code: CodeProviderTimeout, Stage: "provider", Retryable: true,
			Message: "The AI provider took too long to respond. Please try again.",
		}
	default:
		return Classification{
			Code: CodeUnknown, Stage: "unknown", Retryable: true,
			Message: "Something went wrong while generating a response — please try again in a moment.",
		}
	}
}

func firstSentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "Some pages were created, but a later step needs another attempt."
	}
	for _, prefix := range []string{
		"compound partial failure: ",
		"compound step failed: ",
		"compound step interrupted: ",
	} {
		if strings.HasPrefix(strings.ToLower(msg), prefix) {
			msg = strings.TrimSpace(msg[len(prefix):])
			break
		}
	}
	if len(msg) > 280 {
		return msg[:277] + "..."
	}
	return msg
}
