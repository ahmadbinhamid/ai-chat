package handlers

import (
	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// ApplyHandler exposes the explicit apply/discard step of the draft/apply split.
type ApplyHandler struct {
	builder *themebuild.Service
}

func NewApplyHandler(builder *themebuild.Service) *ApplyHandler {
	return &ApplyHandler{builder: builder}
}

type applyRequest struct {
	ThemeSlug string `json:"theme_slug" binding:"required"`
}

// Apply handles POST /chats/:chatId/apply.
func (h *ApplyHandler) Apply(c *gin.Context) {
	var in applyRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}
	// binding:"required" alone doesn't reject a path separator or "..".
	if err := themefs.ValidateThemeSlug(in.ThemeSlug); err != nil {
		respondBindErr(c, err)
		return
	}

	result, err := h.builder.ApplyDraft(c.Request.Context(), auth.TenantID(c), auth.Token(c), c.Param("chatId"), in.ThemeSlug)
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.OK(c, result)
}

// Discard handles POST /chats/:chatId/discard.
func (h *ApplyHandler) Discard(c *gin.Context) {
	result, err := h.builder.DiscardDraft(c.Request.Context(), auth.TenantID(c), c.Param("chatId"))
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.OK(c, result)
}
