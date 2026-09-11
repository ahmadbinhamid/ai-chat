package urlfetch

import (
	"net"
	"strings"
	"testing"
)

func TestValidateURL_Allowed(t *testing.T) {
	for _, u := range []string{
		"https://example.com",
		"http://example.com",
		"https://example.com/path?query=1",
		"https://sub.example.com:8080/",
		"https://example.com:443",
		"https://example.com:8443/page",
	} {
		if _, err := ValidateURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", u, err)
		}
	}
}

func TestValidateURL_Rejected(t *testing.T) {
	for _, u := range []string{
		"",
		"not a url",
		"ftp://example.com",
		"file:///etc/passwd",
		"gopher://example.com",
		"javascript:alert(1)",
		"https://",
		"https://user:pass@example.com",
		strings.Repeat("a", 3000),
	} {
		if _, err := ValidateURL(u); err == nil {
			t.Errorf("expected %q to be rejected, got no error", u)
		}
	}
}

func TestValidateURL_AllowsNonStandardPorts(t *testing.T) {
	// A public host on an unusual port (a staging site, shared hosting) is
	// an ordinary web request, not an SSRF risk — see ValidateURL's own doc
	// comment on why only IsBlockedIP, not a port allowlist, is the real
	// boundary here.
	for _, u := range []string{"https://example.com:6379", "https://example.com:3000", "https://example.com:54321"} {
		if _, err := ValidateURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", u, err)
		}
	}
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",         // loopback
		"::1",               // loopback v6
		"10.0.0.1",          // private
		"172.16.0.1",        // private
		"192.168.1.1",       // private
		"169.254.169.254",   // link-local — cloud metadata endpoint
		"169.254.1.1",       // link-local
		"fe80::1",           // link-local v6
		"fc00::1",           // unique-local v6
		"0.0.0.0",           // unspecified
		"::",                // unspecified
		"224.0.0.1",         // multicast
		"::ffff:127.0.0.1",  // IPv4-mapped loopback
		"::ffff:10.0.0.1",   // IPv4-mapped private
		"100.64.0.1",        // CGNAT (RFC 6598) — start of range
		"100.127.255.254",   // CGNAT — end of range
		"::ffff:100.64.0.1", // IPv4-mapped CGNAT — proves normalization runs before the CIDR checks, not just the net.IP Is* helpers
		"198.18.0.1",        // benchmarking (RFC 2544)
		"240.0.0.1",         // reserved / "Class E" (RFC 1112)
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test setup: %q did not parse as an IP", s)
		}
		if !IsBlockedIP(ip) {
			t.Errorf("expected %q to be blocked", s)
		}
	}

	allowed := []string{
		"8.8.8.8",              // public
		"1.1.1.1",              // public
		"93.184.216.34",        // public (example.com, historically)
		"2606:4700:4700::1111", // public v6 (Cloudflare)
		"100.63.255.255",       // just below CGNAT (RFC 6598) — /10 mask boundary
		"100.128.0.0",          // just above CGNAT — /10 mask boundary
		"198.17.255.255",       // just below benchmarking (RFC 2544) — /15 mask boundary
	}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test setup: %q did not parse as an IP", s)
		}
		if IsBlockedIP(ip) {
			t.Errorf("expected %q to be allowed, got blocked", s)
		}
	}

	if !IsBlockedIP(nil) {
		t.Error("expected a nil IP to be treated as blocked")
	}
}
