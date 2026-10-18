package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/spottr/spottr/internal/auth"
	"github.com/spottr/spottr/internal/config"
)

// The NZB route: the API key, or a user the proxy signed in (header from the trusted proxy only),
// or a session in password mode; never just "can reach the port".
func TestNZBAccess(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("192.168.25.5/32")} // where Traefik's requests arrive from
	const key = "0123456789abcdef"
	cases := []struct {
		name, mode, peer, apikey, header string
		want                             int
	}{
		{"Prowlarr with the key", config.AuthModeProxy, "192.168.10.96", key, "", 200},
		{"browser through Authentik", config.AuthModeProxy, "192.168.25.5", "", "someone", 200},
		{"LAN, no key", config.AuthModeProxy, "192.168.10.96", "", "", 401},
		{"LAN forging the header", config.AuthModeProxy, "192.168.10.96", "", "someone", 401},
		{"proxy without the header", config.AuthModeProxy, "192.168.25.5", "", "", 401},
		{"wrong key", config.AuthModeProxy, "192.168.10.96", "nope", "", 401},
		{"open mode", config.AuthModeOpen, "192.168.10.96", "", "", 200},
		{"password mode, no session", config.AuthModePassword, "192.168.25.5", "", "someone", 401},
		{"password mode, the key", config.AuthModePassword, "192.168.10.96", key, "", 200},
	}
	for _, c := range cases {
		h := &Handler{
			cfg:      &config.Config{AuthMode: c.mode, ProxyAuthHeader: "X-authentik-username", TrustedProxies: trusted},
			apiKey:   key,
			sessions: auth.New(time.Hour),
		}
		ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		srv := realIP(trusted)(h.nzbAccess(ok))

		target := "/v1/spots/1/nzb"
		if c.apikey != "" {
			target += "?apikey=" + c.apikey
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.RemoteAddr = c.peer + ":40000"
		if c.header != "" {
			r.Header.Set("X-authentik-username", c.header)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
	}
}
