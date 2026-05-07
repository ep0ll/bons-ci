---
name: pkg-zap
description: >
  Exhaustive reference for uber-go/zap: logger construction, Sugar vs Core API, fields,
  log levels, sampling, caller info, structured output, child loggers, zap.Error, and
  migration/interop with slog. Use when slog is insufficient (legacy codebase, zap ecosystem).
  Cross-references: packages/slog/SKILL.md, observability/SKILL.md.
---

# Package: uber-go/zap — Complete Reference

## Import
```go
import (
    "go.uber.org/zap"
    "go.uber.org/zap/zapcore"
)
```

## 1. Logger Construction

```go
// Production logger — JSON, sampling, caller info
func NewProductionLogger(level string) (*zap.Logger, error) {
    lvl, err := zapcore.ParseLevel(level)
    if err != nil { return nil, fmt.Errorf("invalid log level %q: %w", level, err) }

    cfg := zap.Config{
        Level:            zap.NewAtomicLevelAt(lvl),
        Development:      false,
        Sampling:         &zap.SamplingConfig{
            Initial:    100,  // first 100 logs per second per level+message
            Thereafter: 100,  // every 100th thereafter
        },
        Encoding:         "json",
        EncoderConfig: zapcore.EncoderConfig{
            TimeKey:        "timestamp",
            LevelKey:       "level",
            NameKey:        "logger",
            CallerKey:      "caller",
            FunctionKey:    zapcore.OmitKey,
            MessageKey:     "message",
            StacktraceKey:  "stacktrace",
            LineEnding:     zapcore.DefaultLineEnding,
            EncodeLevel:    zapcore.LowercaseLevelEncoder,
            EncodeTime:     zapcore.ISO8601TimeEncoder,
            EncodeDuration: zapcore.MillisDurationEncoder,
            EncodeCaller:   zapcore.ShortCallerEncoder,
        },
        OutputPaths:      []string{"stdout"},
        ErrorOutputPaths: []string{"stderr"},
    }
    return cfg.Build(zap.AddCallerSkip(0))
}

// Development logger — human-readable, no sampling
func NewDevLogger() (*zap.Logger, error) {
    return zap.NewDevelopment()
}
```

## 2. Core API vs Sugar

```go
// Core API: allocations-free, strongly typed fields — use in hot paths
log.Info("user registered",
    zap.String("user_id", user.ID),
    zap.String("email", user.Email),
    zap.Int("attempt", 1),
    zap.Duration("elapsed", elapsed),
    zap.Bool("email_sent", true),
    zap.Time("at", time.Now()),
    zap.Error(err),       // zap.Error wraps the error as a named field
    zap.Any("meta", meta), // last resort — use typed fields when possible
)

// Sugar API: variadic key-value — more convenient, slightly slower
sugar := log.Sugar()
sugar.Infow("user registered",
    "user_id", user.ID,
    "email", user.Email,
    "elapsed", elapsed,
)
sugar.Infof("user %s registered", user.ID) // printf-style — avoid in prod (loses structure)
```

## 3. Child Loggers with Persistent Fields

```go
// With: creates child logger with fixed fields — use in middleware
func LoggingMiddleware(base *zap.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            reqLog := base.With(
                zap.String("request_id", getRequestID(r)),
                zap.String("method", r.Method),
                zap.String("path", r.URL.Path),
            )
            ctx := withLogger(r.Context(), reqLog)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// Named: creates child logger with a name prefix
svcLog := base.Named("order_service")
repoLog := svcLog.Named("postgres_repo")
// Log output: {"logger":"order_service.postgres_repo", ...}
```

## 4. Dynamic Level

```go
// AtomicLevel: change log level at runtime without restart
atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
logger := zap.New(zapcore.NewCore(
    zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
    zapcore.Lock(os.Stdout),
    atom,
))

// HTTP endpoint to change level dynamically
mux.Handle("/log-level", atom)  // atom implements http.Handler!
// curl -X PUT localhost:6060/log-level -d '{"level":"debug"}'
```

## 5. zap → slog Bridge (Go 1.21+)

```go
// Use zap as slog backend — bridge for mixed codebases
import "go.uber.org/zap/exp/zapslog"

zapLogger, _ := zap.NewProduction()
slogLogger := slog.New(zapslog.NewHandler(zapLogger.Core(), zapslog.WithCaller(true)))
slog.SetDefault(slogLogger)

// Now slog calls go through zap's core (same output, same sampling)
slog.Info("message", slog.String("key", "value"))
```

## 6. Error Fields

```go
// Always use zap.Error(err) — not zap.String("error", err.Error())
// zap.Error includes the error type in structured output
log.Error("database query failed",
    zap.Error(err),                           // error field
    zap.String("query", sanitizedQuery),
    zap.Duration("duration", elapsed),
)

// Named error field
log.Error("multiple failures",
    zap.NamedError("primary_err", primaryErr),
    zap.NamedError("cleanup_err", cleanupErr),
)
```

## 7. Flushing

```go
// ALWAYS sync before exit — zap may buffer output
defer func() {
    if err := logger.Sync(); err != nil {
        // Sync may fail on stdout/stderr in some environments — suppress common errors
        if !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTTY) {
            fmt.Fprintf(os.Stderr, "logger sync: %v\n", err)
        }
    }
}()
```

## zap Checklist
- [ ] Core API (typed fields) used in hot paths — not Sugar
- [ ] `zap.Error(err)` for error fields — never `zap.String("error", err.Error())`
- [ ] Sampling configured in production — prevent log flooding
- [ ] `logger.Named("service_name")` for component identification
- [ ] `logger.With(...)` in middleware to bind request context to all log lines
- [ ] `defer logger.Sync()` in `main` — flushes buffered output
- [ ] PII, tokens, secrets never in log fields
- [ ] Dynamic level endpoint exposed on internal port only
- [ ] zap→slog bridge used when integrating with slog-based libraries
