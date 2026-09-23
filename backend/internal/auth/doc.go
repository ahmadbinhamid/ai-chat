// Package auth authenticates requests via FlowPOS's /user endpoint — no local users/sessions.
// Only introspection results are cached, never a resolved Identity: caching identity could leak across a tenant switch.
package auth
