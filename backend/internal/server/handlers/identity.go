package handlers

import (
	"net/http"
	"strconv"

	"ai-chat/internal/httpresponse"

	"github.com/gin-gonic/gin"
)

const (
	ctxTenantID = "tenant_id"
	ctxUserID   = "user_id"
	ctxUserName = "user_name"

	hdrTenantID = "X-Tenant-Id"
	hdrUserID   = "X-User-Id"
	hdrUserName = "X-User-Name"
)

// IdentityMiddleware reads the caller-supplied identity headers and stores
// them on the Gin context for downstream handlers (see TenantID/UserID/
// UserName below). This service does no authentication of its own — it's a
// microservice that trusts whatever sits in front of it to have already
// verified the caller (flowPOS's own Bearer token, checked upstream) and to
// forward the identity it established as these headers. There is
// deliberately no signature/token verification here; that responsibility
// belongs to the caller's gateway, not this service.
func IdentityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, err := strconv.ParseUint(c.GetHeader(hdrTenantID), 10, 64)
		if err != nil || tenantID == 0 {
			httpresponse.Error(c, http.StatusBadRequest, "missing or invalid "+hdrTenantID+" header", "MISSING_TENANT")
			c.Abort()
			return
		}

		c.Set(ctxTenantID, tenantID)
		if raw := c.GetHeader(hdrUserID); raw != "" {
			if userID, err := strconv.ParseUint(raw, 10, 64); err == nil && userID != 0 {
				c.Set(ctxUserID, userID)
			}
		}
		c.Set(ctxUserName, c.GetHeader(hdrUserName))
		c.Next()
	}
}

// TenantID reads the caller-supplied tenant ID. Only valid to call on a
// route behind IdentityMiddleware.
func TenantID(c *gin.Context) uint64 {
	return c.GetUint64(ctxTenantID)
}

// UserID reads the caller-supplied user ID, as a pointer so callers can
// distinguish "not sent" (nil) from user ID 0 — matches the
// chat_messages.user_id column, which is nullable for exactly this reason.
func UserID(c *gin.Context) *uint64 {
	v, ok := c.Get(ctxUserID)
	if !ok {
		return nil
	}
	id := v.(uint64)
	return &id
}

// UserName reads the caller-supplied display name. May be "" if the caller
// didn't send one.
func UserName(c *gin.Context) string {
	return c.GetString(ctxUserName)
}
