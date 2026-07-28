package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- fake FlowPOS /user response builders ---
// Named types distinct from wireResponse — tests only need to produce JSON
// text a real Client parses the same way a real FlowPOS response would,
// not to reuse the client's own unmarshal target.

type jsonRole struct {
	ID          uint64   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

type jsonTenant struct {
	ID           uint64   `json:"id"`
	Slug         string   `json:"slug"`
	BusinessName string   `json:"business_name"`
	Role         jsonRole `json:"role"`
}

type jsonUser struct {
	ID       uint64       `json:"id"`
	Name     string       `json:"name"`
	Email    string       `json:"email"`
	IsActive bool         `json:"is_active"`
	Tenants  []jsonTenant `json:"tenants"`
}

type jsonDefaultTenant struct {
	ID uint64 `json:"id"`
}

type jsonEnvelope struct {
	Data struct {
		User          jsonUser           `json:"user"`
		DefaultTenant *jsonDefaultTenant `json:"defaultTenant,omitempty"`
	} `json:"data"`
	Status bool `json:"status"`
}

func introspectHandler(calls *int32, user jsonUser, defaultTenantID *uint64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		var env jsonEnvelope
		env.Status = true
		env.Data.User = user
		if defaultTenantID != nil {
			env.Data.DefaultTenant = &jsonDefaultTenant{ID: *defaultTenantID}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env)
	}
}

func statusHandler(calls *int32, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.WriteHeader(status)
	}
}

func sleepingHandler(calls *int32, d time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		time.Sleep(d)
		w.WriteHeader(http.StatusOK)
	}
}

// --- test engine harness ---

func newTestEngine(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw)
	r.GET("/ping", func(c *gin.Context) {
		identity, _ := FromContext(c)
		c.JSON(http.StatusOK, gin.H{"tenant_id": identity.TenantID, "user_id": identity.UserID})
	})
	return r
}

func doRequest(r *gin.Engine, authHeader, tenantHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if tenantHeader != "" {
		req.Header.Set(hdrTenantID, tenantHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const testToken = "test-token-abc"

var testTenants = []jsonTenant{
	{ID: 384, Slug: "crown", BusinessName: "crown", Role: jsonRole{ID: 112, Name: "second admin", Permissions: []string{"dashboard.view", "orders.view"}}},
	{ID: 501, Slug: "acme", BusinessName: "acme", Role: jsonRole{ID: 200, Name: "owner", Permissions: []string{"dashboard.view"}}},
}

func activeUser(tenants []jsonTenant) jsonUser {
	return jsonUser{ID: 2, Name: "Abu Bakr", Email: "abu@example.com", IsActive: true, Tenants: tenants}
}

func tenantPtr(id uint64) *uint64 { return &id }

// --- scenarios ---

func TestMiddleware_MissingHeader(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "", "")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("expected zero upstream calls, got %d", calls)
	}
}

func TestMiddleware_MalformedHeader(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))

	for _, header := range []string{"token-with-no-bearer-prefix", "Bearer ", "Basic dXNlcjpwYXNz"} {
		w := doRequest(r, header, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", header, w.Code)
		}
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("expected zero upstream calls, got %d", calls)
	}
}

func TestMiddleware_Upstream401(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(statusHandler(&calls, http.StatusUnauthorized))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMiddleware_UpstreamTimeout(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(sleepingHandler(&calls, 200*time.Millisecond))
	defer srv.Close()

	// Client timeout well under the fake server's sleep so this actually
	// exercises a real timeout, not a canned error.
	r := newTestEngine(Middleware(NewClient(srv.URL, 30*time.Millisecond), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestMiddleware_Upstream500(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(statusHandler(&calls, http.StatusInternalServerError))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestMiddleware_NoTenantHeaderUsesDefault(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(501)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]uint64
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["tenant_id"] != 501 {
		t.Fatalf("expected defaultTenant 501 to be resolved, got %v", body["tenant_id"])
	}
}

func TestMiddleware_TenantHeaderOwned(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(501)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "384")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]uint64
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["tenant_id"] != 384 {
		t.Fatalf("expected requested tenant 384 to be resolved, got %v", body["tenant_id"])
	}
}

func TestMiddleware_TenantHeaderNotOwned(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "99999")

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestMiddleware_TenantHeaderMalformed(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "not-a-number")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed X-Tenant-Id, got %d", w.Code)
	}
}

func TestMiddleware_InactiveUser(t *testing.T) {
	var calls int32
	user := activeUser(testTenants)
	user.IsActive = false
	srv := httptest.NewServer(introspectHandler(&calls, user, tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "")

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an inactive user, got %d", w.Code)
	}
}

func TestMiddleware_CacheHitSkipsUpstream(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))

	doRequest(r, "Bearer "+testToken, "")
	doRequest(r, "Bearer "+testToken, "")
	doRequest(r, "Bearer "+testToken, "")

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream call across 3 requests with the same token, got %d", got)
	}
}

func TestMiddleware_NegativeResultCached(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(statusHandler(&calls, http.StatusUnauthorized))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))

	doRequest(r, "Bearer "+testToken, "")
	doRequest(r, "Bearer "+testToken, "")

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected the negative result to be cached (1 upstream call across 2 requests), got %d", got)
	}
}

func TestMiddleware_ExpiredCacheEntryTriggersFreshCall(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	shortTTL := 20 * time.Millisecond
	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), shortTTL, 10*time.Second))

	doRequest(r, "Bearer "+testToken, "")
	time.Sleep(shortTTL + 30*time.Millisecond)
	doRequest(r, "Bearer "+testToken, "")

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected the expired entry to trigger a second upstream call, got %d calls", got)
	}
}

// TestMiddleware_TenantSwitcherOneUpstreamCallTwoTenants is the regression
// test for the caching bug caught in review: a resolved Identity must never
// be cached, only the introspection result, because one token legitimately
// resolves to different tenants across requests (the dashboard's tenant
// switcher). A single upstream call must still serve both tenants correctly.
func TestMiddleware_TenantSwitcherOneUpstreamCallTwoTenants(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))

	w1 := doRequest(r, "Bearer "+testToken, "384")
	w2 := doRequest(r, "Bearer "+testToken, "501")

	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("expected both requests to succeed, got %d and %d", w1.Code, w2.Code)
	}
	var body1, body2 map[string]uint64
	_ = json.Unmarshal(w1.Body.Bytes(), &body1)
	_ = json.Unmarshal(w2.Body.Bytes(), &body2)

	if body1["tenant_id"] != 384 {
		t.Fatalf("expected first request to resolve tenant 384, got %v", body1["tenant_id"])
	}
	if body2["tenant_id"] != 501 {
		t.Fatalf("expected second request to resolve tenant 501, got %v", body2["tenant_id"])
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream call for both tenant switches, got %d", got)
	}
}

// TestMiddleware_CachedTokenUnownedTenantStill403s is the other half of the
// same regression test: a cached (positive) token must still enforce the
// tenant-ownership guard on every request, not just on the first, uncached one.
func TestMiddleware_CachedTokenUnownedTenantStill403s(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), NewMemoryCache(), time.Minute, 10*time.Second))

	w1 := doRequest(r, "Bearer "+testToken, "384")
	if w1.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", w1.Code)
	}

	w2 := doRequest(r, "Bearer "+testToken, "99999")
	if w2.Code != http.StatusForbidden {
		t.Fatalf("expected the cached token with an unowned tenant to still 403, got %d", w2.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected the cache hit to still skip the upstream call, got %d calls", got)
	}
}

func TestMiddleware_CacheErrorFallsBackToLiveCall(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(introspectHandler(&calls, activeUser(testTenants), tenantPtr(384)))
	defer srv.Close()

	r := newTestEngine(Middleware(NewClient(srv.URL, time.Second), alwaysErrorCache{}, time.Minute, 10*time.Second))
	w := doRequest(r, "Bearer "+testToken, "")

	if w.Code != http.StatusOK {
		t.Fatalf("expected a cache failure to degrade to a live upstream call, got %d", w.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream call, got %d", got)
	}
}

// alwaysErrorCache simulates a cache backend that's down — Get/Set always
// error, and Middleware must still succeed by falling through to a live call.
type alwaysErrorCache struct{}

func (alwaysErrorCache) Get(context.Context, string) (CacheEntry, bool, error) {
	return CacheEntry{}, false, errCacheUnavailable
}

func (alwaysErrorCache) Set(context.Context, string, CacheEntry, time.Duration) error {
	return errCacheUnavailable
}

var errCacheUnavailable = errors.New("cache unavailable")
