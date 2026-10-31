package server

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
)

type httpMetrics struct {
	mu       sync.Mutex
	requests map[string]int64
	durSum   map[string]float64
}

func newHTTPMetrics() *httpMetrics {
	return &httpMetrics{requests: map[string]int64{}, durSum: map[string]float64{}}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func routeGroup(p string) string {
	switch {
	case strings.HasPrefix(p, "/api/"):
		return "api"
	case strings.HasPrefix(p, "/static/"), p == "/custom.css", strings.HasPrefix(p, "/avatar/"):
		return "static"
	case strings.HasPrefix(p, "/issues/"):
		return "issue"
	case strings.HasPrefix(p, "/hooks/"):
		return "hooks"
	case strings.HasPrefix(p, "/feeds/"):
		return "feeds"
	case p == "/metrics", p == "/healthz":
		return "internal"
	}
	return "page"
}

func (s *Server) withMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		d := time.Since(start)
		g := routeGroup(r.URL.Path)
		key := fmt.Sprintf(`method="%s",route="%s",status="%d"`, r.Method, g, sw.status)
		s.metrics.mu.Lock()
		s.metrics.requests[key]++
		s.metrics.durSum[g] += d.Seconds()
		s.metrics.mu.Unlock()
		if g != "static" && g != "internal" {
			s.app.Log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "dur", d)
		}
	})
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg()
	if !cfg.Server.Metrics {
		http.NotFound(w, r)
		return
	}
	if tok := cfg.Server.MetricsToken; tok != "" && r.Header.Get("Authorization") != "Bearer "+tok && r.URL.Query().Get("token") != tok {
		http.Error(w, "unauthorized", 401)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	b.WriteString("# TYPE buggy_http_requests_total counter\n")
	s.metrics.mu.Lock()
	keys := make([]string, 0, len(s.metrics.requests))
	for k := range s.metrics.requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "buggy_http_requests_total{%s} %d\n", k, s.metrics.requests[k])
	}
	b.WriteString("# TYPE buggy_http_request_seconds_sum counter\n")
	for g, v := range s.metrics.durSum {
		fmt.Fprintf(&b, "buggy_http_request_seconds_sum{route=%q} %f\n", g, v)
	}
	s.metrics.mu.Unlock()
	for name, vals := range app.Counters() {
		fmt.Fprintf(&b, "# TYPE %s counter\n", name)
		for k, v := range vals {
			fmt.Fprintf(&b, "%s{key=%q} %d\n", name, k, v)
		}
	}
	states, _ := s.app.DB.StateCounts(r.Context(), 0, true)
	b.WriteString("# TYPE buggy_issues gauge\n")
	for st, n := range states {
		fmt.Fprintf(&b, "buggy_issues{state=%q} %d\n", st, n)
	}
	users, _ := s.app.DB.CountUsers(r.Context())
	fmt.Fprintf(&b, "# TYPE buggy_users gauge\nbuggy_users %d\n", users)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(&b, "# TYPE go_goroutines gauge\ngo_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(&b, "# TYPE go_memstats_alloc_bytes gauge\ngo_memstats_alloc_bytes %d\n", mem.Alloc)
	fmt.Fprintf(&b, "# TYPE buggy_uptime_seconds gauge\nbuggy_uptime_seconds %.0f\n", time.Since(startTime).Seconds())
	w.Write([]byte(b.String()))
}
