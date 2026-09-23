package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-chat/internal/httpresponse"

	"github.com/gin-gonic/gin"
)

const (
	ctxIdentityKey = "auth_identity"
	ctxTokenKey    = "auth_token"
	hdrTenantID    = "X-Tenant-Id"
	bearerPrefix   = "Bearer "
)

// Middleware authenticates every request via FlowPOS. The cache lookup only stands in for
// the introspection call — tenant resolution runs fresh every request, or a switch would silently keep serving the old tenant.
func Middleware(client *Client, cache Cache, positiveTTL, negativeTTL time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := extractBearerToken(c.GetHeader("Authorization"))
		if !ok {
			respondUnauthenticated(c, "missing or malformed Authorization header")
			return
		}

		entry, err := lookupOrIntrospect(c.Request.Context(), client, cache, token, positiveTTL, negativeTTL)
		if err != nil {
			// Must not look like an invalid token (401), or an upstream blip logs out every user.
			slog.Default().Error("identity provider unavailable", "error", err.Error(), "request_id", c.GetString("request_id"))
			httpresponse.Error(c, http.StatusServiceUnavailable, "identity provider unavailable, try again shortly", "IDENTITY_UNAVAILABLE")
			c.Abort()
			return
		}
		if entry.Negative {
			respondUnauthenticated(c, "invalid or expired token")
			return
		}
		if !entry.IsActive {
			httpresponse.Error(c, http.StatusForbidden, "account is inactive", "INACTIVE_ACCOUNT")
			c.Abort()
			return
		}

		tenant, status, ok := resolveTenant(c, entry)
		if !ok {
			httpresponse.Error(c, status, tenantErrorMessage(status), tenantErrorCode(status))
			c.Abort()
			return
		}

		c.Set(ctxTokenKey, token)
		c.Set(ctxIdentityKey, Identity{
			UserID:      entry.UserID,
			Name:        entry.Name,
			Email:       entry.Email,
			TenantID:    tenant.ID,
			TenantSlug:  tenant.Slug,
			RoleID:      tenant.RoleID,
			RoleName:    tenant.RoleName,
			Permissions: tenant.Permissions,
		})
		c.Next()
	}
}

func respondUnauthenticated(c *gin.Context, msg string) {
	httpresponse.Error(c, http.StatusUnauthorized, msg, "UNAUTHENTICATED")
	c.Abort()
}

// lookupOrIntrospect returns the cached entry for token if present — a cache error is
// treated as a miss, never surfaced, so a cache outage degrades to a live call, not a 500.
func lookupOrIntrospect(ctx context.Context, client *Client, cache Cache, token string, positiveTTL, negativeTTL time.Duration) (CacheEntry, error) {
	key := cacheKey(token)

	if cached, ok, err := cache.Get(ctx, key); err == nil && ok {
		return cached, nil
	}

	result, err := client.Introspect(ctx, token)
	if errors.Is(err, ErrUnauthorized) {
		entry := CacheEntry{Negative: true}
		_ = cache.Set(ctx, key, entry, negativeTTL)
		return entry, nil
	}
	if err != nil {
		return CacheEntry{}, err
	}

	entry := CacheEntry{
		UserID:          result.UserID,
		Name:            result.Name,
		Email:           result.Email,
		IsActive:        result.IsActive,
		Tenants:         result.Tenants,
		DefaultTenantID: result.DefaultTenantID,
	}
	_ = cache.Set(ctx, key, entry, positiveTTL)
	return entry, nil
}

// cacheKey never stores or logs the raw token — only its sha256 hash.
func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "auth:" + hex.EncodeToString(sum[:])
}

func extractBearerToken(header string) (string, bool) {
	if !strings.HasPrefix(header, bearerPrefix) {
		return "", false
	}
	token := strings.TrimPrefix(header, bearerPrefix)
	if token == "" {
		return "", false
	}
	return token, true
}

// resolveTenant picks which tenant this request acts as. An explicit X-Tenant-Id must be
// one of the user's own tenants — this prevents cross-tenant access via a forged header (IDOR).
func resolveTenant(c *gin.Context, entry CacheEntry) (Tenant, int, bool) {
	return resolveTenantID(c.GetHeader(hdrTenantID), entry)
}

// resolveTenantID is resolveTenant's header-independent core, shared with WebSocketAuth,
// which reads the tenant ID from a subprotocol instead of a header. Same validation either way.
func resolveTenantID(rawTenantID string, entry CacheEntry) (Tenant, int, bool) {
	if rawTenantID != "" {
		id, err := strconv.ParseUint(rawTenantID, 10, 64)
		if err != nil {
			return Tenant{}, http.StatusBadRequest, false
		}
		return findTenant(entry.Tenants, id)
	}
	if entry.DefaultTenantID != nil {
		return findTenant(entry.Tenants, *entry.DefaultTenantID)
	}
	return Tenant{}, http.StatusForbidden, false
}

func findTenant(tenants []Tenant, id uint64) (Tenant, int, bool) {
	for _, t := range tenants {
		if t.ID == id {
			return t, 0, true
		}
	}
	return Tenant{}, http.StatusForbidden, false
}

func tenantErrorMessage(status int) string {
	if status == http.StatusBadRequest {
		return "malformed X-Tenant-Id header"
	}
	return "not a member of the requested tenant"
}

func tenantErrorCode(status int) string {
	if status == http.StatusBadRequest {
		return "INVALID_TENANT_HEADER"
	}
	return "TENANT_FORBIDDEN"
}

// FromContext returns the resolved Identity for the current request, set by
// Middleware. ok is false if called on a route not behind Middleware.
func FromContext(c *gin.Context) (Identity, bool) {
	v, ok := c.Get(ctxIdentityKey)
	if !ok {
		return Identity{}, false
	}
	identity, ok := v.(Identity)
	return identity, ok
}

// TenantID, UserID, and UserName are thin shims over FromContext. New code should prefer FromContext directly.
func TenantID(c *gin.Context) uint64 {
	identity, _ := FromContext(c)
	return identity.TenantID
}

func UserID(c *gin.Context) *uint64 {
	identity, ok := FromContext(c)
	if !ok {
		return nil
	}
	id := identity.UserID
	return &id
}

func UserName(c *gin.Context) string {
	identity, _ := FromContext(c)
	return identity.Name
}

func Email(c *gin.Context) string {
	identity, _ := FromContext(c)
	return identity.Email
}

// Token returns the raw bearer token, for the rare caller that forwards the user's own
// identity to another FlowPOS-authenticated API. Prefer FromContext/TenantID/UserID otherwise.
func Token(c *gin.Context) string {
	v, _ := c.Get(ctxTokenKey)
	token, _ := v.(string)
	return token
}
