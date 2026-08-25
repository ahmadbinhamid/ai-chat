package handlers

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-chat/internal/auth"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

// AssetHandler serves one theme asset's raw bytes (images, fonts — anything
// referenced via the theme engine's asset_url filter), authenticated the
// same way every other route here is. It exists for the frontend's
// client-side LiquidJS preview (tenant-dashboard's liquid-engine.ts):
// asset_url there emits a placeholder path ("/theme-assets/{path}", to stay
// byte-identical with internal/liquidrender's own fixture output — see its
// doc comment), which is meaningless on its own inside a sandboxed preview
// iframe with no real origin to resolve a relative path against.
// flowpos-backend's own public theme-asset route needs a numeric store id
// this service was never given (see PreviewHandler.Context's doc comment on
// the same gap) — this route sidesteps that entirely by reusing the same
// authenticated, auth-scoped file API Store.ReadFile already calls for
// every other theme file, just byte-safe instead of string-safe.
type AssetHandler struct {
	builder *themebuild.Service
}

func NewAssetHandler(builder *themebuild.Service) *AssetHandler {
	return &AssetHandler{builder: builder}
}

// assetContentTypes covers the file kinds a theme actually references via
// asset_url (images, fonts, video — see flowpos-backend's own
// AssetController allow-list, which this mirrors). Not net/mime's
// TypeByExtension: that reads the host OS's mime.types file, which a
// minimal container image may not ship, making content-type detection
// silently flaky in production for exactly the file kinds this route
// exists to serve.
var assetContentTypes = map[string]string{
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
}

// Get handles GET /api/v1/theme-assets/*path.
func (h *AssetHandler) Get(c *gin.Context) {
	relPath := strings.TrimPrefix(c.Param("path"), "/")
	if relPath == "" {
		respondBindErr(c, errors.New("path is required"))
		return
	}

	storeAuth := themefs.RequestAuth{Token: auth.Token(c), TenantID: auth.TenantID(c)}
	data, err := h.builder.ReadThemeAssetBytes(c.Request.Context(), storeAuth, relPath)
	if err != nil {
		respondErr(c, err)
		return
	}
	if data == nil {
		c.Status(http.StatusNotFound)
		return
	}

	// This route has no cheap upstream cache validator to forward — the
	// flowpos-backend file API this proxies (themefs.Store.ReadFileBytes)
	// returns raw content with no hash/ETag/Last-Modified of its own, so the
	// full fetch above always happens; caching here only removes the
	// ai-chat<->browser leg's cost (the repeated re-transfer + re-decode
	// tenant-dashboard's usePreviewDoc.ts otherwise paid on every render),
	// not the ai-chat<->flowpos-backend one.
	//
	// no-cache (not max-age): a path here is a mutable slot, not
	// content-addressed — a merchant can replace an asset at the same path
	// through a completely different flow (the Editor page, straight to
	// flowpos-backend) that this route has no way to invalidate against.
	// max-age would let a browser serve stale bytes for its whole window
	// with zero contact with this server at all; no-cache instead forces a
	// conditional GET on every use, so a change is always picked up on the
	// very next request — the ETag below is what keeps that revalidation
	// cheap (a small 304, not a full re-transfer) rather than costly.
	// private: scoped to the caller's own bearer token/tenant (storeAuth
	// above), not something a shared/intermediate cache should store.
	// Vary on the request headers that actually change what this returns —
	// without it, a browser's cache is keyed on URL alone, and a second
	// tenant/user sharing the same browser profile could otherwise be served
	// the first one's cached bytes for the same path. Note this can't catch
	// every case: if a caller ever omits X-Tenant-Id, auth.Middleware falls
	// back to resolving the tenant server-side (see resolveTenantID), a
	// dimension Vary can't express since the client never sent it — today's
	// only real caller (tenant-dashboard's ai-chat-client.ts) always sends
	// it explicitly, so this is a latent gap, not a live one.
	//
	// Add, not Set/c.Header: this route sits behind CORS middleware
	// (server.go) that already sets its own Vary: Origin on every
	// cross-origin response — which every real call here is, since the
	// dashboard calls this API straight from the browser. c.Header uses Set
	// semantics and would silently clobber that, breaking CORS caching
	// correctness for a completely unrelated reason.
	c.Header("Cache-Control", "private, no-cache")
	c.Header("ETag", themefs.AssetETag(data))
	c.Writer.Header().Add("Vary", "Authorization, X-Tenant-Id")

	contentType, ok := assetContentTypes[extLower(relPath)]
	if !ok {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)

	// http.ServeContent (not c.Data) for RFC 7232-correct conditional-GET:
	// it checks If-None-Match against the ETag header set above itself,
	// handling the comma-separated-list and "*" forms a hand-rolled `==`
	// comparison against a single value silently gets wrong (falling
	// through to a full 200 instead of the spec-correct 304). modtime is
	// the zero value — this route has no Last-Modified from upstream, so
	// only the ETag check applies; ServeContent skips modtime-based
	// validation entirely for a zero time.Time.
	http.ServeContent(c.Writer, c.Request, relPath, time.Time{}, bytes.NewReader(data))
}

func extLower(path string) string {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return ""
	}
	return strings.ToLower(path[i:])
}
