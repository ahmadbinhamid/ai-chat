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

// AssetHandler serves one theme asset's raw bytes for the client-side LiquidJS
// preview, whose asset_url placeholder path needs an authenticated URL to resolve against.
type AssetHandler struct {
	builder *themebuild.Service
}

func NewAssetHandler(builder *themebuild.Service) *AssetHandler {
	return &AssetHandler{builder: builder}
}

// assetContentTypes mirrors flowpos-backend's AssetController allow-list. Not net/mime's
// TypeByExtension: it reads the host's mime.types file, which a minimal container may not ship.
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

	// no-cache: path is a mutable slot, so every use must revalidate via the ETag below.
	// Add (not c.Header/Set) avoids clobbering the CORS middleware's own Vary: Origin.
	c.Header("Cache-Control", "private, no-cache")
	c.Header("ETag", themefs.AssetETag(data))
	c.Writer.Header().Add("Vary", "Authorization, X-Tenant-Id")

	contentType, ok := assetContentTypes[extLower(relPath)]
	if !ok {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)

	// http.ServeContent (not c.Data) handles RFC 7232 conditional-GET correctly;
	// zero modtime skips Last-Modified validation since this route has none from upstream.
	http.ServeContent(c.Writer, c.Request, relPath, time.Time{}, bytes.NewReader(data))
}

func extLower(path string) string {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return ""
	}
	return strings.ToLower(path[i:])
}
