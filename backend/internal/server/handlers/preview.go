package handlers

import (
	"errors"

	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/liquidrender"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// PreviewHandler renders a theme page against fixture data (see
// themefs.FixtureContext) — a merchant can see what a page looks like
// without it ever being saved to the real theme.
type PreviewHandler struct {
	builder *themebuild.Service
}

func NewPreviewHandler(builder *themebuild.Service) *PreviewHandler {
	return &PreviewHandler{builder: builder}
}

type previewRequest struct {
	// Page is a pages.json basename (e.g. "home") — resolves to
	// pages/home.liquid. Ignored if Path or Content is set.
	Page string `json:"page"`
	// Path is an explicit theme-relative path override, e.g.
	// "pages/auth/login.liquid" (for a page under pages/auth/).
	Path string `json:"path"`
	// Content, if set, is rendered as the entry page's literal source
	// instead of whatever currently exists at Path/Page — an unsaved
	// draft, exactly the case a preview exists for. The rest of the
	// theme's real files are still available to it via {% render %}.
	Content string `json:"content"`
}

type previewResponse struct {
	HTML   string   `json:"html"`
	Errors []string `json:"errors"`
}

// Preview handles POST /api/v1/themes/:slug/preview. :slug is accepted for
// API shape/future use but doesn't currently select among multiple themes
// — every themefs.Store call (see its own doc comment) always operates on
// the caller's one active theme, resolved server-side from the tenant, the
// same way every other route in this service already works; ai-chat has
// no notion of a non-active theme to preview instead.
func (h *PreviewHandler) Preview(c *gin.Context) {
	var in previewRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respondBindErr(c, err)
		return
	}

	entryPath := in.Path
	if entryPath == "" && in.Page != "" {
		entryPath = "pages/" + in.Page + ".liquid"
	}
	if entryPath == "" {
		respondBindErr(c, errors.New("one of page, path, or content is required"))
		return
	}

	storeAuth := themefs.RequestAuth{Token: auth.Token(c), TenantID: auth.TenantID(c)}
	files, err := h.builder.LoadThemeFiles(c.Request.Context(), storeAuth)
	if err != nil {
		respondErr(c, err)
		return
	}
	if in.Content != "" {
		files[entryPath] = in.Content
	}

	renderer := liquidrender.Renderer{Files: files}
	html, errs := renderer.Render(entryPath, themefs.FixtureContext())
	if errs == nil {
		errs = []string{}
	}

	httpresponse.OK(c, previewResponse{HTML: html, Errors: errs})
}
