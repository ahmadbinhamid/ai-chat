package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized means FlowPOS rejected the token itself (expired, revoked,
// malformed) — safe to surface to the caller as a real 401.
var ErrUnauthorized = errors.New("flowpos rejected the token")

// ErrUpstreamUnavailable covers everything else that can go wrong talking to
// FlowPOS — timeouts, connection failures, 5xx, an unexpected status code, a
// malformed response body. Deliberately never conflated with ErrUnauthorized:
// a down identity provider must never look like an expired token, or every
// user gets logged out during an upstream blip (see Middleware).
var ErrUpstreamUnavailable = errors.New("flowpos identity provider unavailable")

// Tenant is one entry in user.tenants[] from the introspection response —
// the tenant a request may resolve to, and the role/permissions it carries
// for that specific tenant.
type Tenant struct {
	ID           uint64
	Slug         string
	BusinessName string
	RoleID       uint64
	RoleName     string
	Permissions  []string
}

// IntrospectResult is the parsed /user response for one token. It is the
// exact shape cached (see CacheEntry) — tenant resolution happens later,
// fresh, against Tenants (see package doc comment for why).
type IntrospectResult struct {
	UserID   uint64
	Name     string
	Email    string
	IsActive bool
	Tenants  []Tenant
	// DefaultTenantID is data.defaultTenant.id, or nil if the response had
	// no defaultTenant — used as the tenant to resolve to when the caller
	// sends no X-Tenant-Id.
	DefaultTenantID *uint64
}

// wireResponse mirrors the confirmed /user JSON shape exactly — do not
// guess at or "clean up" this shape, it's copied from a real response.
type wireResponse struct {
	Data struct {
		User struct {
			ID       uint64 `json:"id"`
			Name     string `json:"name"`
			Email    string `json:"email"`
			IsActive bool   `json:"is_active"`
			Tenants  []struct {
				ID           uint64 `json:"id"`
				Slug         string `json:"slug"`
				BusinessName string `json:"business_name"`
				Role         struct {
					ID          uint64   `json:"id"`
					Name        string   `json:"name"`
					Permissions []string `json:"permissions"`
				} `json:"role"`
			} `json:"tenants"`
		} `json:"user"`
		DefaultTenant *struct {
			ID uint64 `json:"id"`
		} `json:"defaultTenant"`
	} `json:"data"`
	Status bool `json:"status"`
}

// Client calls FlowPOS's /user endpoint to verify a bearer token — the only
// source of identity this service trusts.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a Client around one pooled *http.Client, reused across
// every call — never one client per request. timeout bounds the whole
// round trip; there is no retry on any status, including 401 (an invalid
// token doesn't become valid on a second try).
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// Introspect verifies token against FlowPOS and returns the identity data it
// carries. Returns ErrUnauthorized only for an actual 401; every other
// failure mode (timeout, network error, 5xx, an unexpected status, a
// malformed body) returns ErrUpstreamUnavailable instead, wrapped with
// detail via %w/fmt.Errorf so errors.Is still matches.
func (c *Client) Introspect(ctx context.Context, token string) (*IntrospectResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/user", nil)
	if err != nil {
		return nil, fmt.Errorf("build introspection request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstreamUnavailable, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d", ErrUpstreamUnavailable, res.StatusCode)
	}

	var wire wireResponse
	if err := json.NewDecoder(res.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: malformed response body: %w", ErrUpstreamUnavailable, err)
	}

	result := &IntrospectResult{
		UserID:   wire.Data.User.ID,
		Name:     wire.Data.User.Name,
		Email:    wire.Data.User.Email,
		IsActive: wire.Data.User.IsActive,
	}
	for _, t := range wire.Data.User.Tenants {
		result.Tenants = append(result.Tenants, Tenant{
			ID:           t.ID,
			Slug:         t.Slug,
			BusinessName: t.BusinessName,
			RoleID:       t.Role.ID,
			RoleName:     t.Role.Name,
			Permissions:  t.Role.Permissions,
		})
	}
	if wire.Data.DefaultTenant != nil {
		id := wire.Data.DefaultTenant.ID
		result.DefaultTenantID = &id
	}
	return result, nil
}
