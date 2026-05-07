---
name: golang-observability
description: >
  Go observability: structured logging with slog, OpenTelemetry distributed tracing, Prometheus
  metrics, pprof profiling, health checks, graceful shutdown, SLIs/SLOs, alerting hooks, and
  correlation IDs. Use for any Go service that runs in production — observability is not optional.
  Always include when building HTTP servers, gRPC services, background workers, or any daemon.
---

# Go Observability — Production Telemetry

## 1. Structured Logging (slog — standard library, Go 1.21+)

```go
// Initialize: one logger, configured at startup, passed via context or DI
func NewLogger(env string, opts ...slog.HandlerOptions) *slog.Logger {
    var handler slog.Handler
    if env == "production" {
        // JSON for log aggregators (Datadog, Loki, CloudWatch)
        handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
            Level:     slog.LevelInfo,
            AddSource: true,
        })
    } else {
        // Human-readable for local dev
        handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
            Level: slog.LevelDebug,
        })
    }
    return slog.New(handler)
}

// Context-aware logging: attach fields to context, extract in handler
type loggerKey struct{}

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
    return context.WithValue(ctx, loggerKey{}, logger)
}
func LoggerFromContext(ctx context.Context) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok { return l }
    return slog.Default()
}

// HTTP middleware: inject logger with request fields into context
func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            reqLogger := logger.With(
                "request_id", RequestIDFromContext(r.Context()),
                "method",     r.Method,
                "path",       r.URL.Path,
                "remote_ip",  r.RemoteAddr,
            )
            ctx := WithLogger(r.Context(), reqLogger)
            
            start := time.Now()
            rw := &responseWriter{ResponseWriter: w, status: 200}
            next.ServeHTTP(rw, r.WithContext(ctx))
            
            reqLogger.InfoContext(ctx, "request",
                "status",   rw.status,
                "duration", time.Since(start),
                "bytes",    rw.written,
            )
        })
    }
}

// Logging standards:
// - NEVER log credentials, tokens, PII, or passwords
// - ALWAYS use structured fields (slog.String, slog.Int, etc.) not fmt.Sprintf in logger
// - Log at the boundary (handler/adapter), not in domain logic
// - ERROR: unexpected failures requiring investigation
// - WARN:  expected failures worth monitoring
// - INFO:  significant state transitions (server started, job completed)
// - DEBUG: internal state useful during development
```

---

## 2. OpenTelemetry Tracing

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
    "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/attribute"
    semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Initialize tracer provider (call once in main)
func InitTracer(ctx context.Context, serviceName, version string) (func(), error) {
    exporter, err := otlptracehttp.New(ctx)
    if err != nil { return nil, fmt.Errorf("otlp exporter: %w", err) }
    
    tp := trace.NewTracerProvider(
        trace.WithBatcher(exporter),
        trace.WithSampler(trace.ParentBased(trace.TraceIDRatioBased(0.1))), // 10% sampling
        trace.WithResource(resource.NewWithAttributes(
            semconv.SchemaURL,
            semconv.ServiceName(serviceName),
            semconv.ServiceVersion(version),
            attribute.String("env", os.Getenv("ENV")),
        )),
    )
    otel.SetTracerProvider(tp)
    
    return func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        tp.Shutdown(ctx)
    }, nil
}

// Usage in service layer:
var tracer = otel.Tracer("github.com/org/service")

func (s *UserService) Register(ctx context.Context, req RegisterRequest) (*User, error) {
    ctx, span := tracer.Start(ctx, "UserService.Register",
        oteltrace.WithAttributes(
            attribute.String("user.email", req.Email),
        ),
    )
    defer span.End()
    
    user, err := s.repo.Save(ctx, req.toUser())
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return nil, fmt.Errorf("save user: %w", err)
    }
    
    span.SetAttributes(attribute.String("user.id", user.ID.String()))
    span.SetStatus(codes.Ok, "")
    return user, nil
}
```

---

## 3. Prometheus Metrics

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// Define metrics at package level with promauto (auto-registered)
var (
    httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "http_requests_total",
        Help: "Total HTTP requests by method, path, and status code",
    }, []string{"method", "path", "status"})
    
    httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "http_request_duration_seconds",
        Help:    "HTTP request duration in seconds",
        Buckets: prometheus.DefBuckets, // or custom: []float64{.005,.01,.025,.05,.1,.25,.5,1,2.5,5}
    }, []string{"method", "path"})
    
    activeConnections = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "active_connections",
        Help: "Number of currently active connections",
    })
    
    jobQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "job_queue_depth",
        Help: "Number of jobs waiting in queue by type",
    }, []string{"type"})
)

// Metrics middleware
func MetricsMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Normalize path to avoid high-cardinality labels
        route := chi.RouteContext(r.Context()).RoutePattern()
        if route == "" { route = "unknown" }
        
        timer := prometheus.NewTimer(httpRequestDuration.WithLabelValues(r.Method, route))
        rw := &responseWriter{ResponseWriter: w, status: 200}
        
        next.ServeHTTP(rw, r)
        
        timer.ObserveDuration()
        httpRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rw.status)).Inc()
    })
}

// Expose metrics endpoint
mux.Handle("/metrics", promhttp.Handler())
```

---

## 4. Health Checks

```go
// Layered health: liveness (am I alive?) vs readiness (can I serve traffic?)
type HealthChecker interface {
    CheckHealth(ctx context.Context) error
    Name() string
}

type HealthHandler struct {
    checks []HealthChecker
}

func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
    defer cancel()
    
    type checkResult struct {
        Status string `json:"status"`
        Error  string `json:"error,omitempty"`
    }
    
    results := make(map[string]checkResult)
    healthy := true
    
    for _, check := range h.checks {
        if err := check.CheckHealth(ctx); err != nil {
            results[check.Name()] = checkResult{Status: "unhealthy", Error: err.Error()}
            healthy = false
        } else {
            results[check.Name()] = checkResult{Status: "healthy"}
        }
    }
    
    status := http.StatusOK
    if !healthy { status = http.StatusServiceUnavailable }
    
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(map[string]any{"status": map[bool]string{true: "ok", false: "degraded"}[healthy], "checks": results})
}

func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    w.Write([]byte(`{"status":"ok"}`))
}

// DB health check example
type DBHealthCheck struct{ db *sql.DB }
func (c *DBHealthCheck) Name() string { return "database" }
func (c *DBHealthCheck) CheckHealth(ctx context.Context) error {
    return c.db.PingContext(ctx)
}
```

---

## 5. pprof Profiling Endpoints

```go
import _ "net/http/pprof" // registers /debug/pprof handlers

// Serve on separate internal port — never expose to public internet
go func() {
    internal := &http.Server{
        Addr:    ":6060",
        Handler: http.DefaultServeMux, // pprof uses DefaultServeMux
    }
    if err := internal.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        logger.Error("pprof server", "err", err)
    }
}()

// Usage:
// CPU profile:    go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
// Heap profile:   go tool pprof http://localhost:6060/debug/pprof/heap
// Goroutines:     http://localhost:6060/debug/pprof/goroutine?debug=1
// Trace:          curl http://localhost:6060/debug/pprof/trace?seconds=5 > trace.out && go tool trace trace.out
```

---

## 6. Graceful Shutdown with Telemetry Flush

```go
func main() {
    // ... setup ...
    
    // Initialize tracer (returns flush func)
    flushTracer, err := InitTracer(ctx, "my-service", version)
    if err != nil { log.Fatal(err) }
    
    srv := NewHTTPServer(handler)
    
    go func() { srv.ListenAndServe() }()
    
    // Block until signal
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
    sig := <-sigCh
    logger.Info("shutdown signal received", "signal", sig)
    
    // 1. Stop accepting new requests (30s grace period)
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    srv.Shutdown(shutdownCtx)
    
    // 2. Flush telemetry — traces and metrics must ship before exit
    flushTracer()
    
    logger.Info("shutdown complete")
}
```

---

## Observability Checklist

- [ ] Structured JSON logging in production (slog with JSON handler)
- [ ] Correlation/request ID in every log line and trace span
- [ ] No PII, secrets, or tokens logged at any level
- [ ] OpenTelemetry spans on all cross-service calls and DB queries
- [ ] Errors recorded on spans with `span.RecordError(err)`
- [ ] Prometheus metrics: request rate, error rate, duration (RED method)
- [ ] Business metrics: not just infra (e.g., orders_created_total)
- [ ] `/healthz` (liveness) and `/readyz` (readiness) endpoints separate
- [ ] pprof endpoints on internal-only port
- [ ] Graceful shutdown flushes all telemetry before exit
- [ ] SLI/SLO dashboards defined from day 1 (availability, latency p50/p99)
- [ ] Alerts on error rate and latency SLO breach
