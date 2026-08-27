package handlers

import (
	"context"
	"errors"

	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/liquidrender"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// buildPreviewContext is themefs.FixtureContext() with the tenant's real
// store identity overlaid on top (see themebuild.Service.FetchStoreSettings)
// — everything else (products, categories, basket) stays fixture data,
// since those have no single real value the way a store's name does and a
// brand-new tenant may have none yet.
//
// Shared by Preview and Context rather than each calling FixtureContext()
// independently: the frontend's LiquidJS engine (tenant-dashboard's
// liquid-engine.ts) renders against Context's output, and PreviewPane's
// "Check accuracy" button diffs that against Preview's own render — if only
// one of the two carried the real store name, accuracy-check would report a
// false "these differ" on every single page, which is exactly the noise it
// exists to avoid (see liquid-engine.ts's own comment on what it's actually
// meant to catch).
//
// Falls back to the fixture value on fetch failure — a store-settings
// lookup hiccup shouldn't break the whole preview render.
func buildPreviewContext(ctx context.Context, builder *themebuild.Service, storeAuth themefs.RequestAuth) map[string]any {
	fixture := themefs.FixtureContext()
	settings, err := builder.FetchStoreSettings(ctx, storeAuth)
	if err != nil || settings.Name == "" {
		return fixture
	}
	fixture["store"] = map[string]any{"name": settings.Name}
	return fixture
}

// PreviewHandler renders a theme page against fixture data (see
// themefs.FixtureContext) — a merchant can see what a page looks like
// without it ever being saved to the real theme. The frontend's own
// LiquidJS engine (see tenant-dashboard's liquid-engine.ts) now does this
// same rendering client-side, instantly, for the AI chat page's draft
// preview — this endpoint stays as the fidelity reference it renders
// against ("Check accuracy" in PreviewPane.tsx posts the draft here and
// diffs the two HTML outputs), not something replaced by the client-side
// engine. Do not delete this or internal/liquidrender.
type PreviewHandler struct {
	builder *themebuild.Service
}

func NewPreviewHandler(builder *themebuild.Service) *PreviewHandler {
	return &PreviewHandler{builder: builder}
}

type previewRequest struct {
	// Page is a pages.json basename (e.g. "home") — resolves to
	// pages/home.liquid. Ignored if Path is set.
	Page string `json:"page"`
	// Path is an explicit theme-relative path override, e.g.
	// "pages/auth/login.liquid" (for a page under pages/auth/).
	Path string `json:"path"`
	// Files, if set, overlays these theme-relative paths -> content on top
	// of the real theme's own files before rendering — an unsaved draft
	// (potentially many files: pages, components, layout splices), exactly
	// the case a preview/accuracy-check exists for. Superset of the old
	// single-file Content field: a draft is never just one file once a
	// chat's turn touches a component another page also renders.
	Files map[string]string `json:"files"`
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
		respondBindErr(c, errors.New("one of page or path is required"))
		return
	}

	storeAuth := themefs.RequestAuth{Token: auth.Token(c), TenantID: auth.TenantID(c)}
	files, err := h.builder.LoadBaseThemeFiles(c.Request.Context(), storeAuth, false)
	if err != nil {
		respondErr(c, err)
		return
	}
	for path, content := range in.Files {
		files[path] = content
	}

	renderer := liquidrender.Renderer{Files: files}
	html, errs := renderer.Render(entryPath, buildPreviewContext(c.Request.Context(), h.builder, storeAuth))
	if errs == nil {
		errs = []string{}
	}

	httpresponse.OK(c, previewResponse{HTML: html, Errors: errs})
}

// Context handles GET /api/v1/preview/context — themefs.FixtureContext()
// as JSON, so the frontend's LiquidJS preview never hand-copies the
// fixture data into TypeScript (a second, driftable copy of the same
// sample product/category/etc. shape this Go endpoint already owns) and
// instead fetches the one source of truth.
func (h *PreviewHandler) Context(c *gin.Context) {
	storeAuth := themefs.RequestAuth{Token: auth.Token(c), TenantID: auth.TenantID(c)}
	httpresponse.OK(c, buildPreviewContext(c.Request.Context(), h.builder, storeAuth))
}
