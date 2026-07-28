package api

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ─── Client IP ───────────────────────────────────────────────────────────────

// realIP replaces chi's middleware.RealIP, which trusts X-Forwarded-For and
// X-Real-Ip from ANY peer with no trusted-proxy list.
//
// That was directly exploitable: the login lockout keys on the client IP, so an
// attacker could rotate a forged header per request and never trip the
// 5-attempt limit. The same forgery poisons rate limiting and access logs. Here
// the headers are honoured only when the immediate peer is in TRUSTED_PROXIES.
func realIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := resolveClientIP(r, trusted); ip != "" {
				r.RemoteAddr = ip
			}
			next.ServeHTTP(w, r)
		})
	}
}

func resolveClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := hostOnly(r.RemoteAddr)
	if len(trusted) == 0 {
		return peer
	}
	peerAddr, err := netip.ParseAddr(peer)
	if err != nil || !inAny(peerAddr, trusted) {
		return peer
	}

	if xrip := strings.TrimSpace(r.Header.Get("X-Real-Ip")); xrip != "" {
		if _, err := netip.ParseAddr(xrip); err == nil {
			return xrip
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Walk right-to-left and take the first address that is not itself a
		// trusted proxy; anything further left is caller-supplied.
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			addr, err := netip.ParseAddr(candidate)
			if err != nil {
				continue
			}
			if !inAny(addr, trusted) {
				return candidate
			}
		}
	}
	return peer
}

func inAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	addr = addr.Unmap()
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func hostOnly(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// ─── Rate limiting ───────────────────────────────────────────────────────────

// rateLimiter is a per-client token bucket.
//
// Every expensive path here is anonymous by default — search hits FTS over
// hundreds of thousands of rows, and an NZB or image miss opens an upstream
// NNTP connection. Without a limiter a single client can saturate both the
// database and the Usenet account.
type rateLimiter struct {
	rps   float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	if rps <= 0 {
		rps = 5
	}
	if burst <= 0 {
		burst = 30
	}
	rl := &rateLimiter{
		rps:     rps,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
	}
	go rl.cleanup()
	return rl
}

// allow consumes one token for key, reporting whether the request may proceed.
func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		// Bound the map so a spoofed-key flood cannot exhaust memory. Once
		// full we fail open rather than lock out legitimate traffic; the
		// cleanup goroutine drains it within a minute.
		if len(rl.buckets) > 50000 {
			return true
		}
		rl.buckets[key] = &bucket{tokens: rl.burst - 1, last: now}
		return true
	}

	b.tokens += now.Sub(b.last).Seconds() * rl.rps
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *rateLimiter) cleanup() {
	for range time.Tick(time.Minute) {
		cutoff := time.Now().Add(-5 * time.Minute)
		rl.mu.Lock()
		for k, b := range rl.buckets {
			if b.last.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
		rl.mu.Unlock()
	}
}

// middleware returns a handler enforcing the limit, keyed on client IP.
func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(hostOnly(r.RemoteAddr)) {
			metricRateLimited.Add(1)
			w.Header().Set("Retry-After", "1")
			jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Security headers ────────────────────────────────────────────────────────

// securityHeaders adds standard defensive HTTP headers to every response. The
// CSP is computed at startup from the shipped frontend (see csp.go) because
// SvelteKit's inline bootstrap hash changes with every build.
func securityHeaders(policy string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("X-XSS-Protection", "0") // Modern browsers use CSP; this header is legacy
			h.Set("Content-Security-Policy", policy)
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			next.ServeHTTP(w, r)
		})
	}
}

// ─── Request logging ─────────────────────────────────────────────────────────

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// requestLogger emits one structured line per request and feeds the metrics
// counters. Query strings are deliberately omitted: newznab clients put their
// API key in the URL, and logging it would leak the credential.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}

			next.ServeHTTP(sw, r)

			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			dur := time.Since(start)

			metricRequests.Add(1)
			metricRequestSeconds.Add(dur.Seconds())
			if sw.status >= 500 {
				metricServerErrors.Add(1)
			}

			level := slog.LevelInfo
			switch {
			case sw.status >= 500:
				level = slog.LevelError
			case sw.status >= 400:
				level = slog.LevelWarn
			}
			log.Log(r.Context(), level, "http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"bytes", sw.bytes,
				"dur_ms", dur.Milliseconds(),
				"ip", hostOnly(r.RemoteAddr),
			)
		})
	}
}
