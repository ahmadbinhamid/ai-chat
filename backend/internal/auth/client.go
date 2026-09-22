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

// ErrUpstreamUnavailable covers everything else. Never conflated with ErrUnauthorized: a
// down provider must not look like an expired token, or an upstream blip logs everyone out.
var ErrUpstreamUnavailable = errors.New("flowpos identity provider unavailable")

// Tenant is one entry in user.tenants[] from the introspection response.
type Tenant struct {
	ID           uint64
	Slug         string
	BusinessName string
	RoleID       uint64
	RoleName     string
	Permissions  []string
}

// IntrospectResult is the parsed /user response for one token; the exact shape cached.
type IntrospectResult struct {
	UserID   uint64
	Name     string
	Email    string
	IsActive bool
	Tenants  []Tenant
	// DefaultTenantID is used when the caller sends no X-Tenant-Id; nil if the response had none.
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

// NewClient builds a Client around one pooled *http.Client. timeout bounds the round trip;
// there is no retry on any status, including 401.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// Introspect verifies token against FlowPOS. Returns ErrUnauthorized only for an actual 401;
// every other failure returns ErrUpstreamUnavailable.
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
