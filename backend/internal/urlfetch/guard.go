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

// mustParseCIDR panics on a malformed CIDR literal — acceptable only
// because every call site below passes a fixed, compile-time-known string;
// a real parse failure here is a typo in this file, not bad input, and
// would mean IsBlockedIP silently under-blocks for the life of the
// process, which is worse than failing loudly at startup.
func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("urlfetch: invalid CIDR literal %q: %v", s, err))
	}
	return n
}

// Additional non-public IPv4 ranges net.IP's own IsPrivate/
// IsLinkLocalUnicast/IsMulticast/etc. helpers don't cover — parsed once at
// package init, not per call, since IsBlockedIP runs on every dial
// (including every redirect hop), the same reasoning that keeps
// allowedSchemes above a package-level var rather than a literal rebuilt
// each time.
var (
	// cgnatRange is RFC 6598's Shared Address Space, 100.64.0.0/10 —
	// carrier-grade NAT space, NOT covered by net.IP.IsPrivate (which only
	// knows RFC 1918 and RFC 4193). This is a real, currently-used range:
	// Tailscale assigns its own virtual network interfaces addresses out of
	// it, some Kubernetes CNI plugins use it for pod-to-pod addressing, and
	// cloud providers use it for internal routing. If this service ever
	// runs somewhere that addresses internal services out of 100.64/10, an
	// unblocked range here would let a merchant-supplied URL reach one, the
	// exact class of thing this guard exists to prevent.
	cgnatRange = mustParseCIDR("100.64.0.0/10")
	// benchmarkRange is RFC 2544's network-device benchmarking range,
	// 198.18.0.0/15 — reserved for throughput/latency testing of network
	// equipment, never legitimately assigned or routed on the public
	// internet. Not a realistic path into an internal service on its own,
	// but blocking it (and reservedRange below) removes any "why block
	// some reserved ranges and not others" ambiguity for the next reader,
	// at effectively zero cost.
	benchmarkRange = mustParseCIDR("198.18.0.0/15")
	// reservedRange is RFC 1112's "Class E" range, 240.0.0.0/4 — reserved
	// for future use since the original classful-addressing era, never
	// assigned to any network. Same rationale as benchmarkRange: blocked
	// for completeness, not because it's an active attack vector.
	reservedRange = mustParseCIDR("240.0.0.0/4")
)

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
// unspecified, multicast, or otherwise non-public address — every shape of
// "this is inside our own network, not the public internet" an SSRF guard
// needs to reject, including the cloud-metadata endpoint (169.254.169.254,
// inside the link-local range checked here) that's the single most common
// real-world SSRF payload. net.IP's own Is* helpers (IsPrivate in
// particular) cover RFC 1918/4193 but not every non-routable range in
// practice — cgnatRange/benchmarkRange/reservedRange (see their own doc
// comments) fill in the ones that matter. Checked against every IP a
// hostname actually resolves to, not the hostname string itself — a
// hostname can resolve to a different, unsafe address than whatever it
// looked like it would (DNS rebinding), so this must run at connect time
// (see guardedDialer's Control hook in fetch.go), not just once up front
// against the URL's own host string.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Normalize an IPv4-mapped IPv6 address (::ffff:127.0.0.1) to its plain
	// IPv4 form first — net.IP's own Is* helpers already handle this
	// correctly without it, but this keeps the reasoning above (and in
	// tests) about "which literal range is this in" unambiguous, and it has
	// to run before the cgnatRange/benchmarkRange/reservedRange Contains
	// checks below or an IPv4-mapped address in one of those ranges
	// (::ffff:100.64.0.1) would silently fail to match a plain-IPv4 *net.IPNet.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		cgnatRange.Contains(ip) ||
		benchmarkRange.Contains(ip) ||
		reservedRange.Contains(ip)
}
