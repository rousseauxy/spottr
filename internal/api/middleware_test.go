package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustPrefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatalf("bad test CIDR %q: %v", c, err)
		}
		out = append(out, p)
	}
	return out
}

func req(remoteAddr string, headers map[string]string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// The login lockout and the rate limiter both key on the client IP. If a
// forwarding header from an untrusted peer is believed, an attacker rotates it
// per request and neither control ever engages.
func TestResolveClientIPIgnoresUntrustedForwardingHeaders(t *testing.T) {
	trusted := mustPrefixes(t, "10.0.0.0/8")

	got := resolveClientIP(req("203.0.113.9:5555", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"X-Real-Ip":       "5.6.7.8",
	}), trusted)

	if got != "203.0.113.9" {
		t.Errorf("spoofed header was trusted: got %q, want the socket peer 203.0.113.9", got)
	}
}

func TestResolveClientIPWithNoTrustedProxiesAlwaysUsesPeer(t *testing.T) {
	got := resolveClientIP(req("203.0.113.9:5555", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
	}), nil)

	if got != "203.0.113.9" {
		t.Errorf("got %q, want 203.0.113.9", got)
	}
}

func TestResolveClientIPHonoursTrustedProxy(t *testing.T) {
	trusted := mustPrefixes(t, "10.0.0.0/8")

	got := resolveClientIP(req("10.1.2.3:5555", map[string]string{
		"X-Forwarded-For": "198.51.100.7",
	}), trusted)

	if got != "198.51.100.7" {
		t.Errorf("got %q, want 198.51.100.7", got)
	}
}

// With a proxy chain, the rightmost address that is not itself a trusted proxy
// is the real client; anything further left is caller-supplied and forgeable.
func TestResolveClientIPWalksProxyChain(t *testing.T) {
	trusted := mustPrefixes(t, "10.0.0.0/8")

	got := resolveClientIP(req("10.1.2.3:5555", map[string]string{
		"X-Forwarded-For": "1.2.3.4, 198.51.100.7, 10.9.9.9",
	}), trusted)

	if got != "198.51.100.7" {
		t.Errorf("got %q, want 198.51.100.7", got)
	}
}

func TestRateLimiterBlocksBurstOverrun(t *testing.T) {
	rl := newRateLimiter(1, 3)

	for i := 0; i < 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Fatalf("request %d denied while still within burst", i+1)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Error("request past the burst allowance was permitted")
	}
	// A different client has its own bucket.
	if !rl.allow("5.6.7.8") {
		t.Error("unrelated client was rate limited")
	}
}

func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4:5678": "1.2.3.4",
		"[::1]:8080":   "::1",
		"1.2.3.4":      "1.2.3.4",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// The NZB links in Newznab results use the scheme the client used. A forwarded scheme is
// believed only from a trusted proxy.
func TestResolveScheme(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("192.168.10.0/24")}
	req := func(peer, proto string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api?t=search", nil)
		r.RemoteAddr = peer + ":40000"
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		return r
	}
	cases := []struct {
		peer, proto, want string
	}{
		{"192.168.10.5", "", "http"},       // Prowlarr on the LAN address, plain HTTP
		{"192.168.10.5", "https", "https"}, // behind the trusted proxy
		{"203.0.113.9", "https", "http"},   // a forged header from anyone else
		{"192.168.10.5", "gopher", "http"}, // nonsense is ignored
	}
	for _, c := range cases {
		if got := resolveScheme(req(c.peer, c.proto), trusted); got != c.want {
			t.Errorf("resolveScheme(%s, %q) = %s, want %s", c.peer, c.proto, got, c.want)
		}
	}
}
