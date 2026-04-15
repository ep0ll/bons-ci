// Package observability provides structured logging, Prometheus metrics,
// and OpenTelemetry tracing primitives for the BYOC runner platform.
package observability

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const (
	// TenantIDKey is the context key for the current tenant identifier.
	TenantIDKey contextKey = "tenant_id"
	// RunnerIDKey is the context key for the current runner identifier.
	RunnerIDKey contextKey = "runner_id"
	// TraceIDKey is the context key for the distributed trace identifier.
	TraceIDKey contextKey = "trace_id"
	// RequestIDKey is the context key for the HTTP request identifier.
	RequestIDKey contextKey = "request_id"
)

// LogConfig holds logger configuration.
type LogConfig struct {
	Level  string // debug | info | warn | error
	Pretty bool   // human-readable in dev; JSON in prod
}

// NewLogger creates and returns a configured zerolog.Logger.
// In production (pretty=false) logs are emitted as JSON to stdout.
// Every log event should include tenant_id, runner_id, and trace_id.
func NewLogger(cfg LogConfig) zerolog.Logger {
	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339Nano

	var w io.Writer = os.Stdout
	if cfg.Pretty {
		w = zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	}

	return zerolog.New(w).
		With().
		Timestamp().
		Str("service", "byoc-runners").
		Logger()
}

// WithTenantID returns a derived context carrying the tenant ID.
func WithTenantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, TenantIDKey, id)
}

// WithRunnerID returns a derived context carrying the runner ID.
func WithRunnerID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, RunnerIDKey, id)
}

// WithTraceID returns a derived context carrying the trace ID.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, TraceIDKey, id)
}

// WithRequestID returns a derived context carrying the HTTP request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, RequestIDKey, id)
}

// TenantIDFrom extracts the tenant ID from ctx, or returns "".
func TenantIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(TenantIDKey).(string)
	return v
}

// RunnerIDFrom extracts the runner ID from ctx, or returns "".
func RunnerIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(RunnerIDKey).(string)
	return v
}

// TraceIDFrom extracts the trace ID from ctx, or returns "".
func TraceIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(TraceIDKey).(string)
	return v
}

// RequestIDFrom extracts the request ID from ctx, or returns "".
func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(RequestIDKey).(string)
	return v
}

// FromContext enriches a zerolog event with context fields (tenant, runner, trace, request).
// Usage: log.Info().Apply(observability.FromContext(ctx)).Msg("doing something")
func FromContext(ctx context.Context) func(*zerolog.Event) *zerolog.Event {
	return func(e *zerolog.Event) *zerolog.Event {
		if tid := TenantIDFrom(ctx); tid != "" {
			e = e.Str("tenant_id", tid)
		}
		if rid := RunnerIDFrom(ctx); rid != "" {
			e = e.Str("runner_id", rid)
		}
		if trid := TraceIDFrom(ctx); trid != "" {
			e = e.Str("trace_id", trid)
		}
		if reqID := RequestIDFrom(ctx); reqID != "" {
			e = e.Str("request_id", reqID)
		}
		return e
	}
}
