package handlers

import (
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
