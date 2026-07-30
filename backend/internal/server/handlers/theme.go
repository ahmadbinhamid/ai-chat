package handlers

import (
	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// ThemeHandler covers theme-lifecycle actions that aren't part of the chat
// flow itself — currently just starting a brand-new theme from scratch.
type ThemeHandler struct {
	builder *themebuild.Service
}

func NewThemeHandler(builder *themebuild.Service) *ThemeHandler {
	return &ThemeHandler{builder: builder}
}

type createThemeResponse struct {
	Slug string `json:"slug"`
}

// Create installs flowpos-backend's base-theme catalog entry as a new theme
// for the caller's store — used by the "start from scratch" flow before any
// chat message exists for the resulting theme_slug. flowpos-backend assigns
// the slug (see themefs.Store.CreateThemeFromBase); the caller isn't asked
// for one.
func (h *ThemeHandler) Create(c *gin.Context) {
	slug, err := h.builder.CreateThemeFromBase(c.Request.Context(), auth.TenantID(c), auth.Token(c))
	if err != nil {
		respondErr(c, err)
		return
	}

	httpresponse.Created(c, createThemeResponse{Slug: slug})
}
