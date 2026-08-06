package handlers

import (
	"errors"
	"net/http"

	"ai-chat/internal/auth"
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
	// Mode is optional and empty by default (full edit, no restriction) —
	// only the guided "start a theme from scratch" flow should ever send
	// "brand" or "copy" here, and only for that flow's own first two turns.
	// See themebuild.GenerateInput.Mode's doc comment for why this must be
	// explicit rather than inferred from the chat's turn count.
	Mode string `json:"mode" binding:"omitempty,oneof=brand copy edit pages"`
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

// Send accepts the merchant's prompt and returns immediately — the actual
// Claude call and (if the model proposes changes) the write to the real
// theme happen in the background (see themebuild.Service.Generate); the
// caller learns the outcome by polling GET /chat's generating/
// generation_error fields, not from this response. This is still the only
// route with a per-tenant rate limit (see internal/ratelimit): it's the one
// place that ever calls Claude, even though the HTTP call itself is now fast.
func (h *MessageHandler) Send(c *gin.Context) {
	// Bind first: a malformed, oversized, or empty body is the caller's
	// mistake and was never going to reach Claude — it shouldn't also cost
	// the tenant a slot from their per-minute generation budget.
	var in sendMessageRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}

	tenantID := auth.TenantID(c)
	if !h.limiter.Allow(tenantID) {
		httpresponse.Error(c, http.StatusTooManyRequests, "generation rate limit exceeded for this tenant, try again shortly", "RATE_LIMITED")
		return
	}

	outcome, err := h.builder.Generate(c.Request.Context(), themebuild.GenerateInput{
		TenantID:  tenantID,
		UserID:    auth.UserID(c),
		UserName:  auth.UserName(c),
		UserEmail: auth.Email(c),
		Token:     auth.Token(c),
		ThemeSlug: in.ThemeSlug,
		Prompt:    in.Prompt,
		Mode:      in.Mode,
	})
	if err != nil {
		if errors.Is(err, themebuild.ErrGenerationInProgress) {
			httpresponse.Error(c, http.StatusConflict, "a generation is already in progress for this chat", "GENERATION_IN_PROGRESS")
			return
		}
		respondErr(c, err)
		return
	}

	httpresponse.Accepted(c, toResponse(outcome))
}

func toResponse(o themebuild.GenerateOutcome) sendMessageResponse {
	return sendMessageResponse{
		Chat:             o.Chat,
		UserMessage:      o.UserMessage,
		AssistantMessage: o.AssistantMessage,
		Files:            o.Files,
	}
}
