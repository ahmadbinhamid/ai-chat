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

// MessageHandler is the one endpoint that actually calls Claude — sending a
// prompt resolves (or creates) the tenant's one builder chat and, if the
// model proposes any changes, stages them into the chat's draft overlay —
// writing to the real theme is a separate, explicit apply/discard step the
// merchant triggers later (see themebuild's package doc comment).
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
	// only a guided setup flow (walking a merchant through brand, then
	// copy, on a theme they've already installed/activated — the AI
	// theme builder never creates a theme itself) should ever send "brand"
	// or "copy" here, and only for that flow's own first two turns. See
	// themebuild.GenerateInput.Mode's doc comment for why this must be
	// explicit rather than inferred from the chat's turn count.
	Mode string `json:"mode" binding:"omitempty,oneof=brand copy edit pages"`
	// Images attaches up to 5 images to this prompt (matches
	// themebuild.maxImagesPerMessage — kept as a literal here since gin's
	// `max=` binding tag needs one, not a cross-package const expression)
	// — see the image-attachment feature. `max=5,dive` bounds the list AND
	// validates every entry in it. Size/count limits beyond this shape are
	// themebuild.Service.Generate's own job, not this handler's — see its
	// doc comment on why all attachment business rules live there.
	Images []imageAttachment `json:"images" binding:"omitempty,max=5,dive"`
	// HTMLAttachment attaches one reference HTML file to this prompt —
	// see the HTML-attachment feature. Unlike Images, capped at one file
	// (see chat.MessageAttachment's own doc comment, and
	// themebuild.attachmentLimits, for why).
	HTMLAttachment *htmlAttachment `json:"html_attachment" binding:"omitempty"`
}

type htmlAttachment struct {
	Filename string `json:"filename" binding:"required,max=255"`
	Content  string `json:"content" binding:"required"`
}

type imageAttachment struct {
	Base64 string `json:"base64" binding:"required"`
	// MediaType is restricted to exactly the SDK's supported
	// Base64ImageSourceMediaType values.
	MediaType string `json:"media_type" binding:"required,oneof=image/png image/jpeg image/gif image/webp"`
}

type sendMessageResponse struct {
	Chat        any `json:"chat"`
	UserMessage any `json:"user_message"`
	// AssistantMessage is always nil on this response — Generate returns as
	// soon as the prompt is recorded and either kicked off or queued, before
	// Claude has actually replied (see themebuild.GenerateOutcome's doc
	// comment). Still populated in the struct/JSON shape the frontend
	// already expects from GET /chat's history, just never set here; kept on
	// the wire rather than removed so this response shape doesn't change
	// depending on the code path.
	AssistantMessage any `json:"assistant_message"`
	// Files is always nil/empty on this response for the same reason as
	// AssistantMessage above — see themebuild.GenerateOutcome's doc comment.
	// The real files a turn staged arrive later, via GET /chat once the
	// background generation finishes.
	Files any `json:"generated_files"`
	// GenerationID/QueuePosition let the caller track this specific prompt
	// (cancel it while queued, correlate it with stream events) instead of
	// only the chat as a whole — see themebuild.GenerateOutcome's doc
	// comment. QueuePosition 0 means it's running now.
	GenerationID  string `json:"generation_id"`
	QueuePosition int    `json:"queue_position"`
}

// Send accepts the merchant's prompt and always returns 202 immediately —
// the actual Claude call and (if the model proposes changes) staging them
// into the chat's draft overlay happen in the background, either right away
// or once whatever's ahead of it in the queue finishes (see
// themebuild.Service.Generate). There is no "already busy" rejection
// anymore: prompts queue instead (see the queueing brief) — the caller
// learns the outcome by polling GET /chat's `queue` field or the WebSocket
// stream, not from this response. This is still the only route with a
// per-tenant rate limit (see internal/ratelimit): it bounds how fast new
// generations can be *enqueued*, independent of themebuild.ErrQueueFull
// (how many can be *waiting* at once) — the one place that ever calls
// Claude, even though the HTTP call itself is now fast either way.
func (h *MessageHandler) Send(c *gin.Context) {
	// Bind first: a malformed, oversized, or empty body is the caller's
	// mistake and was never going to reach Claude — it shouldn't also cost
	// the tenant a slot from their per-minute generation budget.
	var in sendMessageRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}
	// binding:"required" above only rejects an empty ThemeSlug — it doesn't
	// stop one containing a path separator or ".." from reaching
	// ai.ThemeContext.ThemeSlug (interpolated straight into the model's own
	// system prompt — a prompt-injection surface) and the theme-lock Redis
	// key. ValidateThemeSlug is themefs' own guard for exactly this shape
	// of input (see pathsafety.go) — it just wasn't being called here.
	if err := themefs.ValidateThemeSlug(in.ThemeSlug); err != nil {
		respondBindErr(c, err)
		return
	}

	// Only a wire-format check (is this actually valid base64) — belongs
	// here since it's about whether the request body itself is
	// well-formed, distinct from business rules like size/count limits,
	// which are themebuild.Service.Generate's own job (see its doc
	// comment) so every caller of Generate gets them, not just this one
	// HTTP route.
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
