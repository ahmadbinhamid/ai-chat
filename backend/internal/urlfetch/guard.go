// Package urlfetch fetches a merchant-supplied external URL's HTML content
// server-side, guarded against SSRF (Server-Side Request Forgery) — a URL
// that resolves to this service's own internal network (a cloud metadata
// endpoint, a private database, localhost) instead of a real external
// website. Every function here that decides *whether* a target is safe to
// reach (ValidateURL, IsBlockedIP) is pure — no network access — so it's
// cheaply unit-testable the same way themefs/pathsafety.go is; only
// Fetcher.Fetch (fetch.go) actually touches the network.
package urlfetch

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// maxURLLen bounds the raw URL string itself, before any parsing — a
// pathological input (megabytes of garbage with an http:// prefix) is
// rejected up front rather than handed to url.Parse.
const maxURLLen = 2048

// allowedSchemes is deliberately just the two real web schemes — no
// file://, ftp://, gopher://, or anything else libcurl-era SSRF exploits
// have historically abused to reach unexpected internal services.
var allowedSchemes = map[string]bool{"http": true, "https": true}

// ValidateURL parses raw and rejects anything that isn't a plain, public
// http(s) URL — scheme, userinfo, and host shape only. Deliberately does
// NOT restrict which port a URL may name: the real SSRF boundary is
// IsBlockedIP (checked separately, against the resolved IP, by
// guardedDialer's Control hook), and that already blocks every private/internal
// address regardless of port — a merchant-supplied URL pointing at a
// PUBLIC host on an unusual port (a staging site on :3000, shared hosting
// on :8090) is just an ordinary outbound web request, not a probe of this
// service's own network, so there's nothing extra a port allowlist would
// protect here, only legitimate sites it would reject. This function never
// resolves anything — a hostname's safety can only be judged after DNS
// resolution, not from the URL string alone.
func ValidateURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("url must not be empty")
	}
	if len(raw) > maxURLLen {
		return nil, fmt.Errorf("url is too long (%d characters, max %d)", len(raw), maxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if !allowedSchemes[strings.ToLower(u.Scheme)] {
		return nil, fmt.Errorf("unsupported url scheme %q — only http/https are allowed", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url has no host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("url must not contain userinfo (user:password@)")
	}
	return u, nil
}

// IsBlockedIP reports whether ip is a private, loopback, link-local,
// unspecified, or multicast address — every shape of "this is inside our
// own network, not the public internet" an SSRF guard needs to reject,
// including the cloud-metadata endpoint (169.254.169.254, inside the
// link-local range checked here) that's the single most common real-world
// SSRF payload. Checked against every IP a hostname actually resolves to,
// not the hostname string itself — a hostname can resolve to a different,
// unsafe address than whatever it looked like it would (DNS rebinding), so
// this must run at connect time (see guardedDialer's Control hook in
// fetch.go), not just once up front against the URL's own host string.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Normalize an IPv4-mapped IPv6 address (::ffff:127.0.0.1) to its plain
	// IPv4 form first — net.IP's own Is* helpers already handle this
	// correctly without it, but this keeps the reasoning above (and in
	// tests) about "which literal range is this in" unambiguous.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}
