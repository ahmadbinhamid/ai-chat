package handlers

import (
	"net/http"

	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/ratelimit"

	"github.com/gin-gonic/gin"
)

// clientTimingRatePerMin is a budget of its own: timing reports must never use up a tenant's generation sends.
const clientTimingRatePerMin = 60

type ClientTimingHandler struct {
	builder *themebuild.Service
	limiter *ratelimit.PerTenantLimiter
}

func NewClientTimingHandler(builder *themebuild.Service) *ClientTimingHandler {
	return &ClientTimingHandler{builder: builder, limiter: ratelimit.NewPerTenantLimiter(clientTimingRatePerMin)}
}

// Each value is capped at 10 minutes (600000 ms): longer than any generation runs, so more is a broken clock, not data.
type clientTimingRequest struct {
	PostMs           *uint32 `json:"post_ms" binding:"omitempty,max=600000"`
	WSConnectMs      *uint32 `json:"ws_connect_ms" binding:"omitempty,max=600000"`
	FirstEventMs     *uint32 `json:"first_event_ms" binding:"omitempty,max=600000"`
	FirstProgressMs  *uint32 `json:"first_progress_ms" binding:"omitempty,max=600000"`
	CompletedMs      *uint32 `json:"completed_ms" binding:"omitempty,max=600000"`
	PreviewVisibleMs *uint32 `json:"preview_visible_ms" binding:"omitempty,max=600000"`
}

// Record handles POST /chats/:chatId/generations/:generationId/client-timing.
func (h *ClientTimingHandler) Record(c *gin.Context) {
	tenantID := auth.TenantID(c)
	if !h.limiter.Allow(tenantID) {
		httpresponse.Error(c, http.StatusTooManyRequests, "too many timing reports for this tenant, try again shortly", "RATE_LIMITED")
		return
	}
	var in clientTimingRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}
	err := h.builder.RecordClientTiming(c.Request.Context(), tenantID, c.Param("chatId"), c.Param("generationId"), themebuild.ClientTiming{
		PostMs: in.PostMs, WSConnectMs: in.WSConnectMs, FirstEventMs: in.FirstEventMs,
		FirstProgressMs: in.FirstProgressMs, CompletedMs: in.CompletedMs, PreviewVisibleMs: in.PreviewVisibleMs,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.NoContent(c)
}
