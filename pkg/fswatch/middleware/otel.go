// Package middleware provides cross-cutting concerns for the fanwatch pipeline.
//
// Middleware wraps a [fanwatch.Handler] to add behaviour such as tracing,
// metrics, structured logging, or panic recovery without modifying handlers.
//
// # OTEL Integration
//
// OTELConfig accepts interface types (Tracer, Meter) so this package has zero
// runtime dependency on any specific OTEL SDK. Pass any implementation that
// satisfies the interfaces — go.opentelemetry.io/otel, a noop, or a test spy.
//
// Example wiring with the real OTEL SDK:
//
//	import "go.opentelemetry.io/otel"
//
//	otelMW, _ := middleware.NewOTELWithConfig(middleware.OTELConfig{
//	    Tracer:           otel.Tracer("fanwatch"),
//	    RecordAttributes: true,
//	})
//	pipeline := fanwatch.NewPipeline(
//	    fanwatch.WithMiddleware(otelMW),
//	    fanwatch.WithHandler(myHandler),
//	)
package middleware

import (
	"context"
	"fmt"
	"sync/atomic"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
)

// ─────────────────────────────────────────────────────────────────────────────
// OTEL abstractions — thin interfaces so we have zero import of otel SDK
// ─────────────────────────────────────────────────────────────────────────────

// Span is the subset of trace.Span used by OTELMiddleware.
type Span interface {
	// End completes the span.
	End(...any)
	// RecordError records an error on the span.
	RecordError(error, ...any)
	// SetAttributes adds key-value attributes to the span.
	SetAttributes(attrs ...KV)
}

// Tracer is the subset of trace.Tracer used by OTELMiddleware.
type Tracer interface {
	// Start creates a new span and context.
	Start(ctx context.Context, spanName string, attrs ...KV) (context.Context, Span)
}

// Counter is the subset of metric.Int64Counter used by OTELMiddleware.
type Counter interface {
	// Add increments the counter by n.
	Add(ctx context.Context, n int64, attrs ...KV)
}

// Meter is the minimal factory used by OTELMiddleware.
type Meter interface {
	// Int64Counter creates a named counter.
	Int64Counter(name, description string) (Counter, error)
}

// KV is a simple key-value attribute pair.
// It maps to attribute.KeyValue in the OTEL SDK.
type KV struct {
	Key   string
	Value any
}

// Attr constructs a KV.
func Attr(key string, value any) KV { return KV{Key: key, Value: value} }

// ─────────────────────────────────────────────────────────────────────────────
// OTELConfig
// ─────────────────────────────────────────────────────────────────────────────

// OTELConfig configures [OTELMiddleware].
type OTELConfig struct {
	// Tracer creates spans per event. Nil disables tracing.
	Tracer Tracer

	// Meter creates metric instruments. Nil disables metrics.
	Meter Meter

	// SpanName is the span name. Defaults to "fanwatch.event".
	SpanName string

	// RecordAttributes controls whether event attributes are added to spans
	// and metric labels. Defaults to true.
	RecordAttributes bool
}

// ─────────────────────────────────────────────────────────────────────────────
// OTELMiddleware
// ─────────────────────────────────────────────────────────────────────────────

// OTELMiddleware is a [fanwatch.Middleware] that adds tracing and metrics to
// every handled event.
//
// Spans:
//   - fanwatch.event.mask  — event operation string
//   - fanwatch.event.pid   — triggering PID
//   - fanwatch.event.path  — file path
//   - fanwatch.layer.index — layer stack index (requires OverlayEnricher)
//   - fanwatch.layer.upper — whether the file is in the upperdir
//   - fanwatch.process.comm — process short name (requires ProcessEnricher)
//
// Metrics:
//   - fanwatch.events.total  — counter per event, labelled by mask
//   - fanwatch.events.errors — counter of handler errors
type OTELMiddleware struct {
	cfg    OTELConfig
	events Counter
	errors Counter
}

// NewOTEL constructs an [OTELMiddleware] with a tracer and meter.
// Pass nil to disable either instrument individually.
func NewOTEL(tracer Tracer, meter Meter) (*OTELMiddleware, error) {
	return NewOTELWithConfig(OTELConfig{
		Tracer:           tracer,
		Meter:            meter,
		SpanName:         "fanwatch.event",
		RecordAttributes: true,
	})
}

// NewOTELWithConfig constructs an [OTELMiddleware] from an explicit config.
func NewOTELWithConfig(cfg OTELConfig) (*OTELMiddleware, error) {
	if cfg.SpanName == "" {
		cfg.SpanName = "fanwatch.event"
	}
	m := &OTELMiddleware{cfg: cfg}

	if cfg.Meter != nil {
		var err error
		if m.events, err = cfg.Meter.Int64Counter(
			"fanwatch.events.total",
			"Total fanwatch events handled",
		); err != nil {
			return nil, fmt.Errorf("otel: create events counter: %w", err)
		}
		if m.errors, err = cfg.Meter.Int64Counter(
			"fanwatch.events.errors",
			"Fanwatch handler errors",
		); err != nil {
			return nil, fmt.Errorf("otel: create errors counter: %w", err)
		}
	}
	return m, nil
}

// Wrap implements [fanwatch.Middleware].
func (o *OTELMiddleware) Wrap(next fanwatch.Handler) fanwatch.Handler {
	return fanwatch.HandlerFunc(func(ctx context.Context, e *fanwatch.EnrichedEvent) error {
		ctx, span := o.startSpan(ctx, e)
		defer span.End()

		err := next.Handle(ctx, e)

		o.recordMetrics(ctx, e, err)
		if err != nil {
			span.RecordError(err)
		}
		return err
	})
}

func (o *OTELMiddleware) startSpan(ctx context.Context, e *fanwatch.EnrichedEvent) (context.Context, Span) {
	if o.cfg.Tracer == nil {
		return ctx, noopSpan{}
	}
	var attrs []KV
	if o.cfg.RecordAttributes {
		attrs = eventAttrs(e)
	}
	return o.cfg.Tracer.Start(ctx, o.cfg.SpanName, attrs...)
}

func (o *OTELMiddleware) recordMetrics(ctx context.Context, e *fanwatch.EnrichedEvent, err error) {
	if o.cfg.Meter == nil {
		return
	}
	attrs := eventAttrs(e)
	if o.events != nil {
		o.events.Add(ctx, 1, attrs...)
	}
	if err != nil && o.errors != nil {
		o.errors.Add(ctx, 1, attrs...)
	}
}

func eventAttrs(e *fanwatch.EnrichedEvent) []KV {
	attrs := []KV{
		Attr("fanwatch.event.mask", e.Mask.String()),
		Attr("fanwatch.event.pid", int(e.PID)),
		Attr("fanwatch.event.path", e.Path),
	}
	if e.SourceLayer != nil {
		attrs = append(attrs,
			Attr("fanwatch.layer.index", e.SourceLayer.Index),
			Attr("fanwatch.layer.upper", e.SourceLayer.IsUpper),
		)
	}
	if e.Process != nil {
		attrs = append(attrs, Attr("fanwatch.process.comm", e.Process.Comm))
	}
	return attrs
}

// noopSpan is a do-nothing Span used when no Tracer is configured.
type noopSpan struct{}

func (noopSpan) End(...any)            {}
func (noopSpan) RecordError(error, ...any) {}
func (noopSpan) SetAttributes(...KV)   {}

// ─────────────────────────────────────────────────────────────────────────────
// LoggingMiddleware — structured error logging
// ─────────────────────────────────────────────────────────────────────────────

// LoggingMiddleware logs handler errors with structured context.
// Successful events are not logged here — use [fanwatch.LogHandler] for that.
type LoggingMiddleware struct {
	logFn func(msg string, args ...any)
}

// NewLogging constructs a [LoggingMiddleware].
// logFn matches the signature of [slog.Logger.Error].
func NewLogging(logFn func(msg string, args ...any)) *LoggingMiddleware {
	return &LoggingMiddleware{logFn: logFn}
}

// Wrap implements [fanwatch.Middleware].
func (l *LoggingMiddleware) Wrap(next fanwatch.Handler) fanwatch.Handler {
	return fanwatch.HandlerFunc(func(ctx context.Context, e *fanwatch.EnrichedEvent) error {
		err := next.Handle(ctx, e)
		if err != nil {
			l.logFn("fanwatch: handler error",
				"error", err,
				"path", e.Path,
				"mask", e.Mask.String(),
				"pid", e.PID,
			)
		}
		return err
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// RecoveryMiddleware — catch panics in handlers
// ─────────────────────────────────────────────────────────────────────────────

// RecoveryMiddleware catches panics in the wrapped handler and converts them
// to errors, preventing worker goroutines from crashing.
type RecoveryMiddleware struct {
	onPanic func(recovered any, e *fanwatch.EnrichedEvent)
}

// NewRecovery constructs a [RecoveryMiddleware].
// onPanic is called with the recovered value; it may be nil.
func NewRecovery(onPanic func(recovered any, e *fanwatch.EnrichedEvent)) *RecoveryMiddleware {
	return &RecoveryMiddleware{onPanic: onPanic}
}

// Wrap implements [fanwatch.Middleware].
func (r *RecoveryMiddleware) Wrap(next fanwatch.Handler) fanwatch.Handler {
	return fanwatch.HandlerFunc(func(ctx context.Context, e *fanwatch.EnrichedEvent) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				if r.onPanic != nil {
					r.onPanic(rec, e)
				}
				err = panicToError(rec)
			}
		}()
		return next.Handle(ctx, e)
	})
}

func panicToError(rec any) error {
	switch v := rec.(type) {
	case error:
		return fmt.Errorf("fanwatch: recovered panic: %w", v)
	case string:
		return fmt.Errorf("fanwatch: recovered panic: %s", v)
	default:
		return fmt.Errorf("fanwatch: recovered panic: %v", rec)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MetricsMiddleware — stdlib atomic counters (no OTEL dependency)
// ─────────────────────────────────────────────────────────────────────────────

// MetricsMiddleware tracks basic event statistics using atomic counters.
// Use as a lightweight alternative to OTELMiddleware when full OTEL is not
// available or needed.
type MetricsMiddleware struct {
	handled  atomic.Int64
	errors   atomic.Int64
	filtered atomic.Int64
}

// Wrap implements [fanwatch.Middleware].
func (m *MetricsMiddleware) Wrap(next fanwatch.Handler) fanwatch.Handler {
	return fanwatch.HandlerFunc(func(ctx context.Context, e *fanwatch.EnrichedEvent) error {
		err := next.Handle(ctx, e)
		if err != nil {
			m.errors.Add(1)
		} else {
			m.handled.Add(1)
		}
		return err
	})
}

// Handled returns the count of successfully handled events.
func (m *MetricsMiddleware) Handled() int64 { return m.handled.Load() }

// Errors returns the count of handler errors.
func (m *MetricsMiddleware) Errors() int64 { return m.errors.Load() }

// Reset zeroes all counters.
func (m *MetricsMiddleware) Reset() {
	m.handled.Store(0)
	m.errors.Store(0)
	m.filtered.Store(0)
}
