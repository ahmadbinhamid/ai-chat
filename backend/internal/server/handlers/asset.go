package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

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
	// returns raw content with no hash/ETag/Last-Modified of its own — and a
	// path here is a mutable slot, not content-addressed (a merchant can
	// re-upload the same path with different bytes), so a bare long-lived
	// Cache-Control would risk serving stale content indefinitely. Hashing
	// the bytes ourselves gives a correct strong ETag at near-zero cost next
	// to the network round trip that already happened to fetch them, and
	// turns every repeat request (a dashboard preview reload, a browser
	// refresh) into a 304 instead of re-transferring the full asset —
	// tenant-dashboard's usePreviewDoc.ts fetches every referenced image on
	// every render with no caching of its own before this.
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	// private: scoped to the caller's own bearer token/tenant (storeAuth
	// above), not something a shared/intermediate cache should store.
	// must-revalidate: once max-age lapses, force a conditional GET rather
	// than silently serving something possibly stale — the ETag above is
	// what makes that revalidation a cheap 304 instead of a full re-fetch.
	// Vary on the request headers that actually change what this returns —
	// without it, a browser's cache is keyed on URL alone, and a second
	// tenant/user sharing the same browser profile could otherwise be served
	// the first one's cached bytes for the same path.
	c.Header("Cache-Control", "private, max-age=3600, must-revalidate")
	c.Header("ETag", etag)
	c.Header("Vary", "Authorization, X-Tenant-Id")
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}

	contentType, ok := assetContentTypes[extLower(relPath)]
	if !ok {
		contentType = "application/octet-stream"
	}
	c.Data(http.StatusOK, contentType, data)
}

func extLower(path string) string {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return ""
	}
	return strings.ToLower(path[i:])
}
