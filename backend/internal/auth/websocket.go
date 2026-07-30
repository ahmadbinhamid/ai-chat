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

// WebSocketAuth resolves the caller identity for a browser WebSocket route
// (see server/handlers/stream.go) from Sec-WebSocket-Protocol subprotocols
// instead of the Authorization header Middleware reads — a browser's native
// WebSocket constructor cannot set custom request headers, only the
// subprotocol list passed as its second argument, so the bearer token and
// tenant ID ride there instead: "bearer.<base64url token>" and
// "tenant.<id>". This never appears in the URL (no query-string token to
// leak via server access logs or a Referer header on navigation), unlike
// the alternative of a ?token= query parameter.
//
// This must run and succeed BEFORE the caller upgrades the connection (see
// this route's own doc comment in stream.go for why) — on failure this
// writes the appropriate error response itself (matching Middleware's
// behavior) and returns ok=false; the caller must return immediately
// without upgrading.
func WebSocketAuth(c *gin.Context, client *Client, cache Cache, positiveTTL, negativeTTL time.Duration) (Identity, string, bool) {
	token, rawTenantID, matchedSubprotocol, err := parseWebSocketSubprotocols(c.GetHeader("Sec-WebSocket-Protocol"))
	if err != nil {
		respondUnauthenticated(c, "missing or malformed bearer subprotocol")
		return Identity{}, "", false
	}

	entry, err := lookupOrIntrospect(c.Request.Context(), client, cache, token, positiveTTL, negativeTTL)
	if err != nil {
		// Same reasoning as Middleware: never let an upstream outage look
		// like an invalid token, and never echo err.Error() to the client.
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

// parseWebSocketSubprotocols extracts the bearer token and (optional)
// tenant ID from a Sec-WebSocket-Protocol header value — a comma-separated
// list of the subprotocols the client offered, e.g.
// "bearer.<base64url>, tenant.42". The token is base64url-encoded on the
// wire (RawURLEncoding, no padding) since Sec-WebSocket-Protocol entries
// must be valid HTTP tokens (RFC 2616), which a raw bearer token isn't
// guaranteed to be.
//
// matchedSubprotocol is the exact "bearer.<...>" entry as the client sent
// it (raw, still base64url-encoded) — the caller must echo this back in the
// 101 response's Sec-WebSocket-Protocol header (via
// websocket.AcceptOptions.Subprotocols). The WebSocket spec requires the
// server to select and echo exactly one of the offered subprotocols;
// omitting that header entirely is silently tolerated by Go's own
// coder/websocket client (which never checks it) but is treated as a
// handshake failure by a real browser's WebSocket constructor — so a test
// against the Go client alone would never have caught this.
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
