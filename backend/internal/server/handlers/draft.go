package handlers

import (
	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// DraftHandler exposes a chat's effective file map (real theme + pending
// draft overlay) for the frontend's LiquidJS preview — see
// themebuild.Service.DraftFiles. Deliberately its own route, not folded
// into GET /chat: that payload is already tens of KB (full transcript +
// every generated file's before/after content), and this one can be
// dozens of whole files on top of that for no benefit to a caller that
// only wants the transcript.
type DraftHandler struct {
	builder *themebuild.Service
}

func NewDraftHandler(builder *themebuild.Service) *DraftHandler {
	return &DraftHandler{builder: builder}
}

// Files handles GET /chats/:chatId/draft.
func (h *DraftHandler) Files(c *gin.Context) {
	files, err := h.builder.DraftFiles(c.Request.Context(), auth.TenantID(c), auth.Token(c), c.Param("chatId"))
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.OK(c, files)
}
