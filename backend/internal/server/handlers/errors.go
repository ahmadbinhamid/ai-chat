package handlers

import (
	"errors"
	"net/http"

	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// respondErr maps domain errors to HTTP status codes. Shared by every
// handler in this package — add a new module's sentinel errors here as
// features grow, rather than status-coding them ad hoc per handler.
func respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chat.ErrNotFound), errors.Is(err, themebuild.ErrNotFound):
		httpresponse.Error(c, http.StatusNotFound, err.Error(), "NOT_FOUND")
	case errors.Is(err, themefs.ErrSlugAlreadyRegistered):
		httpresponse.Error(c, http.StatusConflict, err.Error(), "SLUG_ALREADY_REGISTERED")
	default:
		httpresponse.Error(c, http.StatusInternalServerError, err.Error(), "")
	}
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
