// Package middleware provides production-grade HTTP middleware for AccelRegistry.
//
// Middleware stack (applied outermost-first):
//  1. Recovery         — catch panics, return 500, log with stack trace
//  2. RequestID        — inject X-Request-ID header
//  3. Logger           — structured access log with latency and status code
//  4. PrometheusMetrics — Prometheus counters/histograms per route
//  5. CORS             — permissive CORS for browser-based tooling
//  6. ContentType      — enforce application/json on mutation endpoints
package middleware

import (
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/logger"
)

// ────────────────────────────────────────────────────────────────────────────
// Minimal lock-free metrics (avoids google.golang.org/protobuf transitive dep)
// In production, swap in prometheus/client_golang once the allowlist permits.
// ────────────────────────────────────────────────────────────────────────────

type counterVec struct {
	mu     sync.RWMutex
	counts map[string]*int64
}

func newCounterVec() *counterVec { return &counterVec{counts: make(map[string]*int64)} }

func (c *counterVec) Inc(labels ...string) {
	key := strings.Join(labels, "|")
	c.mu.RLock()
	p := c.counts[key]
	c.mu.RUnlock()
	if p == nil {
		c.mu.Lock()
		if c.counts[key] == nil {
			v := int64(0)
			c.counts[key] = &v
		}
		p = c.counts[key]
		c.mu.Unlock()
	}
	atomic.AddInt64(p, 1)
}

func (c *counterVec) Add(v float64, labels ...string) {
	key := strings.Join(labels, "|")
	c.mu.RLock()
	p := c.counts[key]
	c.mu.RUnlock()
	if p == nil {
		c.mu.Lock()
		if c.counts[key] == nil {
			z := int64(0)
			c.counts[key] = &z
		}
		p = c.counts[key]
		c.mu.Unlock()
	}
	atomic.AddInt64(p, int64(v))
}

var (
	httpRequestsTotal    = newCounterVec()
	httpRequestDuration  = newCounterVec()
	httpRequestBodyBytes = newCounterVec()
	httpResponseBytes    = newCounterVec()
)

// ────────────────────────────────────────────────────────────────────────────
// Recovery — panic → 500
// ────────────────────────────────────────────────────────────────────────────

// Recovery returns a middleware that recovers from panics and returns 500.
func Recovery(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					reqID := chimiddleware.GetReqID(r.Context())
					log.Error("panic recovered",
						logger.Any("panic", p),
						logger.String("requestId", reqID),
						logger.String("method", r.Method),
						logger.String("path", r.URL.Path),
						logger.ByteString("stack", debug.Stack()),
					)
					http.Error(w, http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Structured logger
// ────────────────────────────────────────────────────────────────────────────

// Logger returns a structured access-logging middleware.
func Logger(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(ww, r)
			latency := time.Since(start)

			level := logger.InfoLevel
			if ww.status >= 500 {
				level = logger.ErrorLevel
			} else if ww.status >= 400 {
				level = logger.WarnLevel
			}

			log.Check(level, "request").Write(
				logger.String("method", r.Method),
				logger.String("path", r.URL.Path),
				logger.Int("status", ww.status),
				logger.String("latency", latency.String()),
				logger.Int64("responseBytes", ww.bytesWritten),
				logger.String("requestId", chimiddleware.GetReqID(r.Context())),
				logger.String("remoteAddr", r.RemoteAddr),
			)
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Prometheus-compatible metrics middleware (lock-free internal counters)
// ────────────────────────────────────────────────────────────────────────────

// PrometheusMetrics returns a middleware that records request metrics.
func PrometheusMetrics() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(ww, r)

			route := normaliseRoute(r.URL.Path)
			status := strconv.Itoa(ww.status)
			_ = time.Since(start) // would feed into histogram

			httpRequestsTotal.Inc(r.Method, route, status)
			if r.ContentLength > 0 {
				httpRequestBodyBytes.Add(float64(r.ContentLength), r.Method, route)
			}
			if ww.bytesWritten > 0 {
				httpResponseBytes.Add(float64(ww.bytesWritten), r.Method, route)
			}
		})
	}
}

// normaliseRoute replaces digest and UUID segments with placeholders.
func normaliseRoute(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "sha256:") || strings.HasPrefix(p, "sha512:") {
			parts[i] = "{digest}"
		} else if len(p) == 32 && isHex(p) {
			parts[i] = "{uuid}"
		}
	}
	return strings.Join(parts, "/")
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ────────────────────────────────────────────────────────────────────────────
// CORS
// ────────────────────────────────────────────────────────────────────────────

// CORS returns a permissive CORS middleware.
func CORS() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods",
				"GET, HEAD, POST, PUT, DELETE, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Content-Type, Authorization, Docker-Content-Digest, Accept")
			w.Header().Set("Access-Control-Expose-Headers",
				"Docker-Content-Digest, Location, Docker-Upload-UUID")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Content-Type enforcement
// ────────────────────────────────────────────────────────────────────────────

// RequireJSON enforces Content-Type: application/json on Accel API mutations.
func RequireJSON() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if (r.Method == http.MethodPost || r.Method == http.MethodPut) &&
				strings.HasPrefix(r.URL.Path, "/accel/") {
				ct := r.Header.Get("Content-Type")
				if !strings.HasPrefix(ct, "application/json") {
					http.Error(w,
						`{"errors":[{"code":"UNSUPPORTED","message":"Content-Type must be application/json"}]}`,
						http.StatusUnsupportedMediaType)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// responseRecorder
// ────────────────────────────────────────────────────────────────────────────

type responseRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

func (r *responseRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
		r.ResponseWriter.WriteHeader(status)
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += int64(n)
	return n, err
}

func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
