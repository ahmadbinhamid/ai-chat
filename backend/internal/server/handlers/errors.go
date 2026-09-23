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

// respondErr maps domain errors to HTTP status codes. The default branch logs the raw
// error server-side and returns a generic message, so nothing leaks internal detail to the client.
func respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chat.ErrNotFound), errors.Is(err, themebuild.ErrNotFound):
		httpresponse.Error(c, http.StatusNotFound, err.Error(), "NOT_FOUND")
	// GENERATION_IN_PROGRESS lives on purely for revert's "can't revert while something
	// is writing to this theme" case; Generate itself no longer returns this error.
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
	case errors.Is(err, themebuild.ErrVisionNotConfigured):
		httpresponse.Error(c, http.StatusUnprocessableEntity, err.Error(), "VISION_NOT_CONFIGURED")
	case errors.Is(err, themebuild.ErrTooManyImages):
		httpresponse.Error(c, http.StatusUnprocessableEntity, err.Error(), "TOO_MANY_IMAGES")
	case errors.Is(err, themebuild.ErrImageTooLarge):
		httpresponse.Error(c, http.StatusRequestEntityTooLarge, err.Error(), "IMAGE_TOO_LARGE")
	case errors.Is(err, themebuild.ErrHTMLAttachmentTooLarge):
		httpresponse.Error(c, http.StatusRequestEntityTooLarge, err.Error(), "HTML_ATTACHMENT_TOO_LARGE")
	case errors.Is(err, themebuild.ErrLinkFetchFailed):
		httpresponse.Error(c, http.StatusUnprocessableEntity, err.Error(), "LINK_FETCH_FAILED")
	// Checked before the generic default so a merchant sees an actionable "try again" message
	// instead of a raw upstream status (e.g. Cloudflare 521) that reads like a bug in this app.
	case isUpstreamUnavailable(err):
		slog.Default().Warn("upstream (FlowPOS) unavailable", "error", err.Error(), "request_id", logging.RequestID(c))
		httpresponse.Error(c, http.StatusServiceUnavailable,
			"the theme service is temporarily unavailable — please try again in a moment", "UPSTREAM_UNAVAILABLE")
	default:
		slog.Default().Error("unhandled request error", "error", err.Error(), "request_id", logging.RequestID(c))
		httpresponse.Error(c, http.StatusInternalServerError, "an unexpected error occurred", "")
	}
}

// isUpstreamUnavailable matches known upstream-down/Cloudflare status codes by substring, since themefs errors are plain strings, not typed.
func isUpstreamUnavailable(err error) bool {
	lower := strings.ToLower(err.Error())
	for _, code := range []string{"502", "503", "520", "521", "522", "523", "524"} {
		if strings.Contains(lower, code) {
			return true
		}
	}
	return false
}

// respondBindErr maps binding failures to responses: field validation errors as a 422 {field: message} map, unparseable body as 400.
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
