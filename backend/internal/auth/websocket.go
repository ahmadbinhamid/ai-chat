package auth

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ai-chat/internal/httpresponse"

	"github.com/gin-gonic/gin"
)

// WebSocketAuth resolves identity from Sec-WebSocket-Protocol subprotocols, since a browser WebSocket can't set headers.
// Must succeed before the caller upgrades; on failure it responds itself and returns ok=false — caller must not upgrade.
func WebSocketAuth(c *gin.Context, client *Client, cache Cache, positiveTTL, negativeTTL time.Duration) (Identity, string, bool) {
	token, rawTenantID, matchedSubprotocol, err := parseWebSocketSubprotocols(c.GetHeader("Sec-WebSocket-Protocol"))
	if err != nil {
		respondUnauthenticated(c, "missing or malformed bearer subprotocol")
		return Identity{}, "", false
	}

	entry, err := lookupOrIntrospect(c.Request.Context(), client, cache, token, positiveTTL, negativeTTL)
	if err != nil {
		// Never let an upstream outage look like an invalid token; never echo err to the client.
		slog.Default().Error("identity provider unavailable", "error", err.Error(), "request_id", c.GetString("request_id"))
		httpresponse.Error(c, http.StatusServiceUnavailable, "identity provider unavailable, try again shortly", "IDENTITY_UNAVAILABLE")
		c.Abort()
		return Identity{}, "", false
	}
	if entry.Negative {
		respondUnauthenticated(c, "invalid or expired token")
		return Identity{}, "", false
	}
	if !entry.IsActive {
		httpresponse.Error(c, http.StatusForbidden, "account is inactive", "INACTIVE_ACCOUNT")
		c.Abort()
		return Identity{}, "", false
	}

	tenant, status, ok := resolveTenantID(rawTenantID, entry)
	if !ok {
		httpresponse.Error(c, status, tenantErrorMessage(status), tenantErrorCode(status))
		c.Abort()
		return Identity{}, "", false
	}

	return Identity{
		UserID:      entry.UserID,
		Name:        entry.Name,
		Email:       entry.Email,
		TenantID:    tenant.ID,
		TenantSlug:  tenant.Slug,
		RoleID:      tenant.RoleID,
		RoleName:    tenant.RoleName,
		Permissions: tenant.Permissions,
	}, matchedSubprotocol, true
}

// parseWebSocketSubprotocols extracts the bearer token and tenant ID from the comma-separated
// Sec-WebSocket-Protocol header; matchedSubprotocol must be echoed back in the 101 response —
// a real browser fails the handshake if it's omitted, though Go's own client tolerates that.
func parseWebSocketSubprotocols(header string) (token string, tenantID string, matchedSubprotocol string, err error) {
	if header == "" {
		return "", "", "", errors.New("missing Sec-WebSocket-Protocol header")
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "bearer."):
			decoded, decErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(part, "bearer."))
			if decErr != nil || len(decoded) == 0 {
				return "", "", "", errors.New("invalid bearer subprotocol")
			}
			token = string(decoded)
			matchedSubprotocol = part
		case strings.HasPrefix(part, "tenant."):
			tenantID = strings.TrimPrefix(part, "tenant.")
		}
	}
	if token == "" {
		return "", "", "", errors.New("missing bearer subprotocol")
	}
	return token, tenantID, matchedSubprotocol, nil
}
