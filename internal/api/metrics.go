package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

func float64ToBits(f float64) uint64   { return math.Float64bits(f) }
func float64FromBits(b uint64) float64 { return math.Float64frombits(b) }

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// Metrics are exposed in Prometheus text format from stdlib primitives rather
// than by pulling in the Prometheus client library. The counter set here is
// small and fixed, and the audit flagged dependency surface as a risk worth
// keeping down — an observability endpoint is not worth a new supply chain.

type atomicFloat struct{ bits atomic.Uint64 }

func (f *atomicFloat) Add(v float64) {
	for {
		old := f.bits.Load()
		neu := float64FromBits(old) + v
		if f.bits.CompareAndSwap(old, float64ToBits(neu)) {
			return
		}
	}
}

func (f *atomicFloat) Load() float64 { return float64FromBits(f.bits.Load()) }

var (
	metricRequests       atomic.Int64
	metricServerErrors   atomic.Int64
	metricRateLimited    atomic.Int64
	metricRequestSeconds atomicFloat

	metricNZBFetches   atomic.Int64
	metricNZBCacheHits atomic.Int64
	metricNZBFailures  atomic.Int64
	metricNNTPDialFail atomic.Int64
	metricAuthFailures atomic.Int64

	processStart = time.Now()
)

// metricsHandler renders the counters. It is intentionally unauthenticated only
// when explicitly bound to a private listener; here it sits behind the same
// auth as the rest of the app (see Router).
func (h *Handler) metricsHandler(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	spots, _ := h.db.CountSpots(r.Context())
	cacheBytes, _ := h.db.CacheBytes(r.Context())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	write := func(name, typ, help string, value any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, value)
	}

	write("spottr_http_requests_total", "counter", "Total HTTP requests served.", metricRequests.Load())
	write("spottr_http_server_errors_total", "counter", "HTTP responses with status >= 500.", metricServerErrors.Load())
	write("spottr_http_rate_limited_total", "counter", "Requests rejected by the rate limiter.", metricRateLimited.Load())
	write("spottr_http_request_seconds_total", "counter", "Cumulative request handling time.", metricRequestSeconds.Load())
	write("spottr_auth_failures_total", "counter", "Failed authentication attempts.", metricAuthFailures.Load())

	write("spottr_nzb_fetches_total", "counter", "NZBs fetched from Usenet.", metricNZBFetches.Load())
	write("spottr_nzb_cache_hits_total", "counter", "NZBs served from cache.", metricNZBCacheHits.Load())
	write("spottr_nzb_failures_total", "counter", "NZB fetch failures.", metricNZBFailures.Load())
	write("spottr_nntp_dial_failures_total", "counter", "Failed NNTP connection attempts.", metricNNTPDialFail.Load())

	write("spottr_spots", "gauge", "Number of spots in the database.", spots)
	write("spottr_cache_bytes", "gauge", "Bytes held in the NZB and image caches.", cacheBytes)
	write("spottr_goroutines", "gauge", "Current goroutine count.", runtime.NumGoroutine())
	write("spottr_memory_heap_bytes", "gauge", "Heap bytes in use.", ms.HeapAlloc)
	write("spottr_uptime_seconds", "gauge", "Seconds since process start.", int64(time.Since(processStart).Seconds()))
}

// healthz is a liveness probe: cheap, dependency-light, and never authenticated
// so the container healthcheck keeps working once the newznab API requires a
// key.
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

// readyz additionally confirms the database answers.
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()

	if err := h.db.PingContext(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ready")
}
