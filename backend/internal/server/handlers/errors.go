package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"ai-chat/internal/httpresponse"
	"ai-chat/internal/logging"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// respondErr maps domain errors to HTTP status codes. Shared by every
// handler in this package — add a new module's sentinel errors here as
// features grow, rather than status-coding them ad hoc per handler.
//
// The default branch is the only place an error this codebase didn't
// anticipate ends up — a raw DB error, a filesystem error, anything not
// explicitly mapped above. That's exactly the kind of error that can leak
// internal detail (table/column names, filesystem paths) if sent to the
// client verbatim, and — until this fix — it was also never logged
// anywhere server-side, meaning the HTTP response was the *only* record of
// it ever happening. Log the real error here, return a generic one.
func respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chat.ErrNotFound), errors.Is(err, themebuild.ErrNotFound):
		httpresponse.Error(c, http.StatusNotFound, err.Error(), "NOT_FOUND")
	// themebuild.ErrGenerationInProgress itself is no longer one of the
	// cases here — Generate never returns it anymore (a prompt that can't
	// run immediately queues instead of being rejected, see Generate's doc
	// comment), and nothing else surfaces it to a handler either. The
	// GENERATION_IN_PROGRESS code lives on purely for revert's still-valid
	// "can't revert while something is writing to this theme" case.
	case errors.Is(err, themebuild.ErrRevertBlockedByRunningGeneration):
		httpresponse.Error(c, http.StatusConflict, err.Error(), "GENERATION_IN_PROGRESS")
	case errors.Is(err, themebuild.ErrQueueFull):
		httpresponse.Error(c, http.StatusTooManyRequests, err.Error(), "QUEUE_FULL")
	case errors.Is(err, themebuild.ErrApplyBlockedByRunningGeneration):
		httpresponse.Error(c, http.StatusConflict, err.Error(), "APPLY_IN_PROGRESS")
	case errors.Is(err, themebuild.ErrNoPendingChanges):
		httpresponse.Error(c, http.StatusConflict, err.Error(), "NO_PENDING_CHANGES")
	case errors.Is(err, themebuild.ErrManualEditFileNotFound):
		httpresponse.Error(c, http.StatusNotFound, err.Error(), "FILE_NOT_FOUND")
	// isUpstreamUnavailable is checked before the generic default branch —
	// a merchant seeing "the theme service is temporarily unavailable,
	// please try again" (a real, actionable, non-scary explanation) instead
	// of "an unexpected error occurred" (which reads exactly like a bug in
	// this app, prompting a support ticket / log-diving for something that
	// was actually FlowPOS or the network briefly hiccupping) is the whole
	// point of this branch. Observed in production: GET /chats/:id/draft
	// failing with a raw "unexpected status 521" (Cloudflare's own
	// "origin down" code — see themefs' statusErr) from a theme-file read,
	// with nothing surfaced to the merchant beyond the generic message —
	// this is that fix.
	case isUpstreamUnavailable(err):
		slog.Default().Warn("upstream (FlowPOS) unavailable", "error", err.Error(), "request_id", logging.RequestID(c))
		httpresponse.Error(c, http.StatusServiceUnavailable,
			"the theme service is temporarily unavailable — please try again in a moment", "UPSTREAM_UNAVAILABLE")
	default:
		slog.Default().Error("unhandled request error", "error", err.Error(), "request_id", logging.RequestID(c))
		httpresponse.Error(c, http.StatusInternalServerError, "an unexpected error occurred", "")
	}
}

// isUpstreamUnavailable reports whether err looks like FlowPOS (or the
// network path to it) being transiently down, rather than a genuine bug in
// this service. themefs' own errors are plain fmt.Errorf-wrapped strings
// (see disk.go's statusErr — "unexpected status %d: %s"), not a typed
// status code this could switch on structurally, so this matches the same
// way ai.categorizeError's own fallback does: on the recognizable status
// codes as substrings. 502/503 are standard "upstream down" codes; 520-524
// are Cloudflare's own origin-unreachable/timeout family (see
// developers.cloudflare.com/support/troubleshooting/http-status-codes) —
// seen in practice sitting in front of flowpos-backend.
func isUpstreamUnavailable(err error) bool {
	lower := strings.ToLower(err.Error())
	for _, code := range []string{"502", "503", "520", "521", "522", "523", "524"} {
		if strings.Contains(lower, code) {
			return true
		}
	}
	return false
}

// respondBindErr handles request-body binding failures: field validation
// failures become a 422 with a {field: message} map, an unparseable body a
// 400.
func respondBindErr(c *gin.Context, err error) {
	var ve validator.ValidationErrors
	if errors.As(err, &ve) {
		fields := make(map[string]string, len(ve))
		for _, fe := range ve {
			fields[fe.Field()] = validationMessage(fe)
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"errors": fields})
		return
	}
	httpresponse.Error(c, http.StatusBadRequest, err.Error(), "")
}

func validationMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return fe.Field() + " is required"
	case "max":
		return fe.Field() + " must be at most " + fe.Param() + " characters"
	default:
		return fe.Field() + " is invalid"
	}
}
