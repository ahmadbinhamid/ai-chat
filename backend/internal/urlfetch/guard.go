// Package urlfetch fetches a merchant-supplied external URL's HTML,
// guarded against SSRF (a URL resolving to our own internal network — cloud
// metadata, a private DB, localhost). ValidateURL/IsBlockedIP are pure and
// unit-testable; only Fetcher.Fetch touches the network.
package urlfetch

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// maxURLLen rejects a pathological input up front, before url.Parse.
const maxURLLen = 2048

// allowedSchemes excludes file://, ftp://, gopher://, etc. — classic SSRF vectors.
var allowedSchemes = map[string]bool{"http": true, "https": true}

// mustParseCIDR panics on a bad literal — every caller passes a fixed
// compile-time string, so a failure means a typo here, and failing loudly
// at startup beats IsBlockedIP silently under-blocking for the process's life.
func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("urlfetch: invalid CIDR literal %q: %v", s, err))
	}
	return n
}

// Extra non-public IPv4 ranges net.IP's IsPrivate/IsLinkLocalUnicast/etc.
// don't cover. Parsed once at package init since IsBlockedIP runs on every dial.
var (
	// cgnatRange: RFC 6598 Shared Address Space (100.64.0.0/10), not covered by
	// net.IP.IsPrivate. Actively used by Tailscale, some k8s CNI plugins, and
	// cloud internal routing — an unblocked range here would defeat the guard.
	cgnatRange = mustParseCIDR("100.64.0.0/10")
	// benchmarkRange: RFC 2544 network-benchmarking range, never routed publicly.
	benchmarkRange = mustParseCIDR("198.18.0.0/15")
	// reservedRange: RFC 1112 "Class E" (240.0.0.0/4), never assigned to any network.
	reservedRange = mustParseCIDR("240.0.0.0/4")
)

// ValidateURL checks scheme/userinfo/host shape only — no port restriction,
// since the real SSRF boundary is IsBlockedIP against the resolved IP
// (guardedDialer's Control hook), which already blocks private/internal
// addresses regardless of port. Never resolves the hostname itself.
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

// IsBlockedIP reports whether ip is private/loopback/link-local/unspecified/
// multicast/etc — including 169.254.169.254, the cloud-metadata endpoint and
// most common real SSRF payload. Must be checked at connect time against the
// resolved IP (see guardedDialer's Control hook), not once against the
// hostname string, since DNS rebinding can repoint a hostname after the fact.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Normalize an IPv4-mapped IPv6 address (::ffff:127.0.0.1) to IPv4 first —
	// must happen before the CIDR Contains checks below or an IPv4-mapped
	// address in one of those ranges would fail to match the plain-IPv4 *net.IPNet.
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
