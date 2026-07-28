package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-chat/internal/httpresponse"

	"github.com/gin-gonic/gin"
)

const (
	ctxIdentityKey = "auth_identity"
	hdrTenantID    = "X-Tenant-Id"
	bearerPrefix   = "Bearer "
)

// Middleware authenticates every request by delegating to FlowPOS — see the
// package doc comment. It never mints its own token or session; the cache
// TTL is the only revocation window this service has.
//
// Order matters here: the cache lookup only ever stands in for the
// introspection call. is_active and tenant resolution run on every request,
// cache hit or miss — never cached themselves — because the same token can
// legitimately arrive with a different X-Tenant-Id across requests (a
// tenant switcher), and caching the resolved tenant would silently keep
// serving the first one for the rest of the TTL.
func Middleware(client *Client, cache Cache, positiveTTL, negativeTTL time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := extractBearerToken(c.GetHeader("Authorization"))
		if !ok {
			respondUnauthenticated(c, "missing or malformed Authorization header")
			return
		}

		entry, err := lookupOrIntrospect(c.Request.Context(), client, cache, token, positiveTTL, negativeTTL)
		if err != nil {
			// Upstream unreachable/erroring and nothing usable was cached —
			// this must never look like an invalid token (401), or an
			// upstream blip logs out every user.
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

// lookupOrIntrospect returns the cached entry for token if present — a
// cache error is treated the same as a miss (never surfaced to the caller;
// a cache outage must degrade to a live call, not a 500) — otherwise calls
// FlowPOS and caches the outcome, positive or negative, before returning it.
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

// resolveTenant picks which tenant this request acts as. An explicit
// X-Tenant-Id must be one of the user's own tenants — this is what prevents
// cross-tenant access via a forged header (an IDOR), so it's never trusted
// on its own. A malformed header (not a valid uint64) is the caller's
// mistake, distinct from a well-formed id that just isn't theirs — 400, not
// 403. Absent, it falls back to defaultTenant, itself re-validated against
// the same list.
func resolveTenant(c *gin.Context, entry CacheEntry) (Tenant, int, bool) {
	if raw := c.GetHeader(hdrTenantID); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
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

// TenantID, UserID, and UserName are thin shims over FromContext, kept with
// these exact names and signatures so the existing handler call sites (see
// server/handlers/message.go, chat.go) only needed a new import, not a
// rewrite, when this package replaced the old placeholder identity
// middleware. New code should prefer FromContext directly.
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
