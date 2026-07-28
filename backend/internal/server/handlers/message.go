package handlers

import (
	"net/http"

	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/ratelimit"

	"github.com/gin-gonic/gin"
)

// MessageHandler is the one endpoint that actually calls Claude — sending a
// prompt resolves (or creates) the tenant's one builder chat and, if the
// model proposes any changes, writes them to the real theme immediately.
type MessageHandler struct {
	builder *themebuild.Service
	limiter *ratelimit.PerTenantLimiter
}

func NewMessageHandler(builder *themebuild.Service, limiter *ratelimit.PerTenantLimiter) *MessageHandler {
	return &MessageHandler{builder: builder, limiter: limiter}
}

type sendMessageRequest struct {
	ThemeSlug string `json:"theme_slug" binding:"required"`
	// max=6000 (runes): generous for even a detailed page description, but
	// enough to reject an accidental paste of an entire document before it
	// burns input tokens on a request nobody meant to send.
	Prompt string `json:"prompt" binding:"required,max=6000"`
}

type sendMessageResponse struct {
	Chat             any `json:"chat"`
	UserMessage      any `json:"user_message"`
	AssistantMessage any `json:"assistant_message"`
	// Files is a record of what was written — see themebuild.Service.Generate,
	// which applies these to the real theme in the same request rather than
	// waiting for a separate "Apply to theme" call.
	Files any `json:"generated_files"`
}

// Send calls Claude with the merchant's prompt and returns the resulting
// turn — this is the only route in the service with a per-tenant rate limit
// (see internal/ratelimit): every other endpoint is a cheap DB read/write,
// this one costs real money and multi-second latency per call.
func (h *MessageHandler) Send(c *gin.Context) {
	tenantID := TenantID(c)
	if !h.limiter.Allow(tenantID) {
		httpresponse.Error(c, http.StatusTooManyRequests, "generation rate limit exceeded for this tenant, try again shortly", "RATE_LIMITED")
		return
	}

	var in sendMessageRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}

	outcome, err := h.builder.Generate(c.Request.Context(), themebuild.GenerateInput{
		TenantID:  tenantID,
		UserID:    UserID(c),
		UserName:  UserName(c),
		ThemeSlug: in.ThemeSlug,
		Prompt:    in.Prompt,
	})
	if err != nil {
		// A recorded-but-failed turn still has an outcome to show the
		// caller (see themebuild.Service.Generate) — render it, but as an
		// error status so the client knows the turn didn't succeed.
		if outcome.Chat.ID != "" {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": err.Error(),
				"data":  toResponse(outcome),
			})
			return
		}
		respondErr(c, err)
		return
	}

	httpresponse.OK(c, toResponse(outcome))
}

func toResponse(o themebuild.GenerateOutcome) sendMessageResponse {
	return sendMessageResponse{
		Chat:             o.Chat,
		UserMessage:      o.UserMessage,
		AssistantMessage: o.AssistantMessage,
		Files:            o.Files,
	}
}
