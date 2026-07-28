// Package auth authenticates every request by delegating to FlowPOS's own
// /user endpoint — this service has no users table, no login route, no
// password handling, and mints no token or session of its own. A bearer
// token issued by the tenant dashboard is forwarded upstream verbatim on
// every uncached request; FlowPOS's response is the only source of truth
// for who the caller is and which tenants they may act as.
//
// The introspection RESULT is what gets cached (see CacheEntry), never a
// resolved Identity — a single token can legitimately be re-sent with a
// different X-Tenant-Id across requests (the dashboard's tenant switcher),
// so tenant resolution runs fresh against the cached-or-live tenant list on
// every request, cache hit or not. Caching the resolved Identity instead
// would silently pin a token to whichever tenant it first resolved to for
// the rest of the cache TTL — a cross-tenant leak, not just a staleness bug.
//
// Two accepted tradeoffs, not overlooked:
//  1. N concurrent requests racing in on the same cold (uncached) token each
//     trigger their own upstream call — a thundering herd on cache miss.
//     singleflight would collapse these into one; not worth the dependency
//     at this service's traffic today.
//  2. Revoking a token upstream doesn't take effect here instantly — a
//     revoked token keeps working against this service for up to
//     AUTH_CACHE_TTL_SECONDS. That lag is the deliberate cost of caching at
//     all; turn the TTL down if the window matters more than upstream call
//     volume.
package auth
