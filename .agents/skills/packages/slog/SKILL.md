---
name: pkg-slog
description: >
  Exhaustive reference for Go's log/slog (standard library, Go 1.21+): logger creation,
  handler selection, structured attributes, context-aware logging, custom handlers, log levels,
  group attributes, source location, performance patterns, and migration from log/logrus/zap.
  Primary logging package for all new services. Cross-references: observability/SKILL.md.
---

# Package: log/slog — Complete Reference

## Import
```go
import "log/slog"
```

## 1. Logger Setup

```go
// Production: JSON handler (for log aggregators)
func NewProductionLogger(level slog.Level) *slog.Logger {
    return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level:     level,
        AddSource: true,  // adds "source":{"function":"...","file":"...","line":N}
        ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
            // Rename "time" → "timestamp" for Datadog/Loki compatibility
            if a.Key == slog.TimeKey { a.Key = "timestamp" }
            // Rename "msg" → "message" for ECS compatibility
            if a.Key == slog.MessageKey { a.Key = "message" }
            return a
        },
    }))
}

// Development: text handler (human-readable)
func NewDevLogger() *slog.Logger {
    return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
        Level:     slog.LevelDebug,
        AddSource: true,
    }))
}

// Set as default (for legacy log.Printf calls)
slog.SetDefault(logger)
```

## 2. Logging Patterns

```go
// Key-value pairs (slog.Attr)
slog.Info("user registered",
    slog.String("user_id", user.ID),
    slog.String("email", user.Email),
    slog.Int("attempt", 1),
    slog.Duration("elapsed", elapsed),
    slog.Bool("email_sent", true),
    slog.Time("registered_at", user.CreatedAt),
    slog.Any("metadata", meta),
)

// Context-aware (preferred — extracts trace ID, request ID from context)
slog.InfoContext(ctx, "order created",
    slog.String("order_id", order.ID()),
    slog.String("customer_id", order.CustomerID()),
)

// Error logging — always include err as slog.Any("err", err) or slog.String
slog.ErrorContext(ctx, "payment failed",
    slog.String("order_id", orderID),
    slog.Any("err", err),  // preserves error type for structured output
)

// Levels
slog.Debug(...)   // development details
slog.Info(...)    // significant events
slog.Warn(...)    // recoverable issues
slog.Error(...)   // errors requiring investigation
// No Fatal/Panic — handle in main() after getting error return
```

## 3. Logger with Persistent Attributes

```go
// With: create child logger with fixed attributes
// Use in middleware to bind request context to all log lines in that request
func LoggingMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            reqLogger := base.With(
                slog.String("request_id", middleware.GetReqID(r.Context())),
                slog.String("method", r.Method),
                slog.String("path", r.URL.Path),
                slog.String("remote_ip", r.RemoteAddr),
            )
            ctx := context.WithValue(r.Context(), loggerKey{}, reqLogger)

            start := time.Now()
            rw := wrapResponseWriter(w)
            next.ServeHTTP(rw, r.WithContext(ctx))

            reqLogger.InfoContext(ctx, "request completed",
                slog.Int("status", rw.status),
                slog.Int64("bytes", rw.written),
                slog.Duration("duration", time.Since(start)),
            )
        })
    }
}

// Service logger with service-level context
type OrderService struct {
    log *slog.Logger
}

func NewOrderService(base *slog.Logger) *OrderService {
    return &OrderService{
        log: base.WithGroup("order_service"),  // prefixes all attrs: "order_service.xxx"
    }
}
```

## 4. Context Logger Pattern

```go
type loggerKey struct{}

func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
    return context.WithValue(ctx, loggerKey{}, l)
}

// Always returns a usable logger — never nil
func L(ctx context.Context) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok { return l }
    return slog.Default()
}

// Usage
slog.L(ctx).InfoContext(ctx, "processing order", slog.String("id", id))
```

## 5. Groups (namespace attributes)

```go
// Group: prefix a set of attributes with a namespace
slog.Info("request",
    slog.Group("http",
        slog.String("method", "POST"),
        slog.String("path", "/users"),
        slog.Int("status", 201),
    ),
    slog.Group("user",
        slog.String("id", userID),
        slog.String("email", email),
    ),
)
// Output: {"http":{"method":"POST","path":"/users","status":201},"user":{...}}
```

## 6. Custom Handler (e.g., add trace IDs from context)

```go
// Handler that extracts OpenTelemetry trace/span IDs from context
type OTelHandler struct {
    base slog.Handler
}

func (h *OTelHandler) Handle(ctx context.Context, r slog.Record) error {
    span := trace.SpanFromContext(ctx)
    if span.SpanContext().IsValid() {
        r.AddAttrs(
            slog.String("trace_id", span.SpanContext().TraceID().String()),
            slog.String("span_id",  span.SpanContext().SpanID().String()),
        )
    }
    return h.base.Handle(ctx, r)
}

func (h *OTelHandler) Enabled(ctx context.Context, level slog.Level) bool { return h.base.Enabled(ctx, level) }
func (h *OTelHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return &OTelHandler{h.base.WithAttrs(attrs)} }
func (h *OTelHandler) WithGroup(name string) slog.Handler { return &OTelHandler{h.base.WithGroup(name)} }

// Usage
logger := slog.New(&OTelHandler{base: slog.NewJSONHandler(os.Stdout, opts)})
```

## 7. Performance

```go
// ── ENABLED CHECK for expensive attribute construction ─────────
if slog.Default().Enabled(ctx, slog.LevelDebug) {
    slog.DebugContext(ctx, "cache dump", slog.Any("entries", expensiveDump()))
}

// ── LogAttrs: avoids allocation for []any conversion ────────────
// slog.Info() uses []any internally → allocates
// slog.Default().LogAttrs() avoids that allocation
logger.LogAttrs(ctx, slog.LevelInfo, "user registered",
    slog.String("id", id),
    slog.String("email", email),
)

// ── Pre-allocated attrs for repeated use ────────────────────────
serviceAttr := slog.String("service", "order-service")
logger = logger.With(serviceAttr)  // added once, not per log call
```

## 8. What NOT to Log

```go
// ✗ NEVER log: passwords, tokens, keys, PII, credit cards
slog.Info("login", slog.String("password", req.Password)) // NEVER

// ✗ NEVER log full request body blindly
slog.Info("request", slog.Any("body", requestBody)) // may contain secrets

// ✓ Log sanitized versions only
slog.Info("login attempted", slog.String("email", maskEmail(req.Email)))

// ✓ Log errors at the boundary, not at every layer
// Each layer wraps + returns — only log at the handler/adapter level
```

## slog Checklist
- [ ] JSON handler in production, text handler in development
- [ ] `ReplaceAttr` used to rename keys for aggregator compatibility
- [ ] `AddSource: true` in production for log-to-code navigation
- [ ] `slog.ErrorContext` with `slog.Any("err", err)` for all errors
- [ ] Sensitive data (passwords, tokens, PII) never in log messages
- [ ] `WithGroup` used to namespace service-level attributes
- [ ] Context logger extracted via helper `L(ctx)` — never nil return
- [ ] OTel handler wraps base handler to inject trace/span IDs
- [ ] `Enabled` check before expensive attribute computation
