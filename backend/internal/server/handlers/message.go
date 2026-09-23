package handlers

import (
	"encoding/base64"
	"net/http"

	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/ratelimit"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// MessageHandler is the one endpoint that calls the AI provider; it stages proposed changes into the draft overlay, not the real theme.
type MessageHandler struct {
	builder *themebuild.Service
	limiter *ratelimit.PerTenantLimiter
}

func NewMessageHandler(builder *themebuild.Service, limiter *ratelimit.PerTenantLimiter) *MessageHandler {
	return &MessageHandler{builder: builder, limiter: limiter}
}

type sendMessageRequest struct {
	ThemeSlug string `json:"theme_slug" binding:"required"`
	// max=6000 runes rejects an accidental full-document paste before it burns input tokens.
	Prompt string `json:"prompt" binding:"required,max=6000"`
	// Mode is empty by default (full edit). Only the guided brand/copy setup flow's
	// first two turns should ever send "brand" or "copy", explicitly rather than inferred from turn count.
	Mode string `json:"mode" binding:"omitempty,oneof=brand copy edit pages"`
	// Up to 5 images (literal, not themebuild.maxImagesPerMessage, since gin's max= tag needs a literal).
	// max=5,dive bounds and validates the list; size/count business limits are enforced in Generate.
	Images []imageAttachment `json:"images" binding:"omitempty,max=5,dive"`
	// HTMLAttachment allows at most one reference HTML file, unlike Images.
	HTMLAttachment *htmlAttachment `json:"html_attachment" binding:"omitempty"`
}

type htmlAttachment struct {
	Filename string `json:"filename" binding:"required,max=255"`
	Content  string `json:"content" binding:"required"`
}

type imageAttachment struct {
	Base64 string `json:"base64" binding:"required"`
	// MediaType must match the SDK's supported Base64ImageSourceMediaType values.
	MediaType string `json:"media_type" binding:"required,oneof=image/png image/jpeg image/gif image/webp"`
}

type sendMessageResponse struct {
	Chat        any `json:"chat"`
	UserMessage any `json:"user_message"`
	// AssistantMessage is always nil — Generate returns before the model replies. Kept on the wire for shape-stability with GET /chat.
	AssistantMessage any `json:"assistant_message"`
	// Files is always nil/empty for the same reason — real files arrive later via GET /chat.
	Files any `json:"generated_files"`
	// GenerationID/QueuePosition let the caller track this prompt; QueuePosition 0 means running now.
	GenerationID  string `json:"generation_id"`
	QueuePosition int    `json:"queue_position"`
}

// Send accepts a prompt and always returns 202 immediately; the AI call and draft staging happen in the background.
// Prompts queue rather than reject when busy. The per-tenant rate limit here bounds enqueue rate, distinct from ErrQueueFull's wait-queue cap.
func (h *MessageHandler) Send(c *gin.Context) {
	// Bind first so a malformed/oversized body doesn't cost the tenant a rate-limit slot.
	var in sendMessageRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}
	// binding:"required" doesn't block a slug with a path separator or ".." — ThemeSlug flows into the
	// model's system prompt (injection surface) and the theme-lock Redis key, so it needs this explicit guard.
	if err := themefs.ValidateThemeSlug(in.ThemeSlug); err != nil {
		respondBindErr(c, err)
		return
	}

	// Only a wire-format check; size/count business limits are enforced in Generate so every caller gets them.
	for _, img := range in.Images {
		if _, err := base64.StdEncoding.DecodeString(img.Base64); err != nil {
			respondBindErr(c, err)
			return
		}
	}
	images := make([]chat.MessageImage, len(in.Images))
	for i, img := range in.Images {
		images[i] = chat.MessageImage{Base64: img.Base64, MediaType: img.MediaType}
	}

	var htmlFilename, htmlContent *string
	if in.HTMLAttachment != nil {
		htmlFilename = &in.HTMLAttachment.Filename
		htmlContent = &in.HTMLAttachment.Content
	}

	tenantID := auth.TenantID(c)
	if !h.limiter.Allow(tenantID) {
		httpresponse.Error(c, http.StatusTooManyRequests, "generation rate limit exceeded for this tenant, try again shortly", "RATE_LIMITED")
		return
	}

	outcome, err := h.builder.Generate(c.Request.Context(), themebuild.GenerateInput{
		TenantID:               tenantID,
		UserID:                 auth.UserID(c),
		UserName:               auth.UserName(c),
		UserEmail:              auth.Email(c),
		Token:                  auth.Token(c),
		ThemeSlug:              in.ThemeSlug,
		Prompt:                 in.Prompt,
		Mode:                   in.Mode,
		Images:                 images,
		HTMLAttachmentFilename: htmlFilename,
		HTMLAttachmentContent:  htmlContent,
	})
	if err != nil {
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
		GenerationID:     o.GenerationID,
		QueuePosition:    o.QueuePosition,
	}
}
