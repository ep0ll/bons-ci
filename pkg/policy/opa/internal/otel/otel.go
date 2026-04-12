// Package otel provides lightweight OpenTelemetry bootstrap helpers used across
// all layers of the policy system. It deliberately avoids importing domain
// packages so it can be consumed from any tier without creating import cycles.
//
// Every subsystem calls otel.Tracer("subsystem") and otel.Meter("subsystem")
// to obtain its own instrumented handles. These delegate to whatever
// TracerProvider / MeterProvider has been registered by the application.
// In tests, Register(NoopProvider()) silences all telemetry with zero overhead.
package otel

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	// Namespace is the prefix for all metric and span names emitted by this module.
	Namespace = "buildkit.policy"
)

// ─── Attribute keys ──────────────────────────────────────────────────────────

var (
	AttrPolicyKind  = attribute.Key("policy.kind")
	AttrAction      = attribute.Key("policy.action")
	AttrMutated     = attribute.Key("policy.mutated")
	AttrIdentifier  = attribute.Key("source.identifier")
	AttrOpID        = attribute.Key("dag.op_id")
	AttrOpType      = attribute.Key("dag.op_type")
	AttrMatrixSize  = attribute.Key("matrix.size")
	AttrTransformer = attribute.Key("transform.name")
	AttrError       = semconv.ExceptionTypeKey
	AttrEventKind   = attribute.Key("event.kind")
)

// ─── Provider registry ────────────────────────────────────────────────────────

var (
	globalMu    sync.RWMutex
	globalMeter metric.MeterProvider = noop.NewMeterProvider()
)

// SetMeterProvider installs mp as the global meter provider for this package.
// Call this once during application bootstrap, before any policy evaluation.
func SetMeterProvider(mp metric.MeterProvider) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalMeter = mp
}

// Tracer returns a named tracer from the global OTEL TracerProvider.
// name should be the fully-qualified package path, e.g. "pkg/eval".
func Tracer(name string) trace.Tracer {
	return otel.GetTracerProvider().Tracer(
		fmt.Sprintf("%s/%s", Namespace, name),
		trace.WithInstrumentationVersion("1.0.0"),
	)
}

// Meter returns a named meter from the registered global MeterProvider.
func Meter(name string) metric.Meter {
	globalMu.RLock()
	mp := globalMeter
	globalMu.RUnlock()
	return mp.Meter(fmt.Sprintf("%s/%s", Namespace, name))
}

// ─── Span helpers ─────────────────────────────────────────────────────────────

// StartSpan starts a child span, adds attrs, and returns ctx + span + end func.
// The end func records any non-nil error and calls span.End().
// Usage: ctx, end := otel.StartSpan(ctx, tracer, "op", attrs...); defer end(&err)
func StartSpan(
	ctx context.Context,
	tracer trace.Tracer,
	name string,
	attrs ...attribute.KeyValue,
) (context.Context, func(*error)) {
	ctx, span := tracer.Start(ctx, fmt.Sprintf("%s.%s", Namespace, name),
		trace.WithAttributes(attrs...),
	)
	return ctx, func(errPtr *error) {
		if errPtr != nil && *errPtr != nil {
			span.RecordError(*errPtr)
			span.SetStatus(otelcodes.Error, (*errPtr).Error())
		} else {
			span.SetStatus(otelcodes.Ok, "")
		}
		span.End()
	}
}

// AddEvent records a named event on the span in ctx with optional attrs.
func AddEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	trace.SpanFromContext(ctx).AddEvent(name, trace.WithAttributes(attrs...))
}

// RecordError records err on the span and sets the span status to error.
// Safe to call with nil (no-op).
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
}

// ─── Noop helpers for tests ───────────────────────────────────────────────────

// UseNoop installs no-op tracer and meter providers so tests produce no telemetry.
func UseNoop() {
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	SetMeterProvider(noop.NewMeterProvider())
}

// ─── Common metric constructors ───────────────────────────────────────────────

// Counters holds the standard counters used by multiple packages.
type Counters struct {
	Evaluations metric.Int64Counter
	Mutations   metric.Int64Counter
	Denials     metric.Int64Counter
	Expansions  metric.Int64Counter
	Errors      metric.Int64Counter
}

// NewCounters constructs a Counters set registered under the given subsystem name.
func NewCounters(subsystem string) (Counters, error) {
	m := Meter(subsystem)
	prefix := Namespace + "." + subsystem

	eval, err := m.Int64Counter(prefix+".evaluations_total",
		metric.WithDescription("Total policy evaluations"))
	if err != nil {
		return Counters{}, fmt.Errorf("otel: counter %s.evaluations: %w", subsystem, err)
	}
	mut, err := m.Int64Counter(prefix+".mutations_total",
		metric.WithDescription("Total ops mutated by policy"))
	if err != nil {
		return Counters{}, fmt.Errorf("otel: counter %s.mutations: %w", subsystem, err)
	}
	den, err := m.Int64Counter(prefix+".denials_total",
		metric.WithDescription("Total ops denied by policy"))
	if err != nil {
		return Counters{}, fmt.Errorf("otel: counter %s.denials: %w", subsystem, err)
	}
	exp, err := m.Int64Counter(prefix+".expansions_total",
		metric.WithDescription("Total expansions (DAG/matrix)"))
	if err != nil {
		return Counters{}, fmt.Errorf("otel: counter %s.expansions: %w", subsystem, err)
	}
	errC, err := m.Int64Counter(prefix+".errors_total",
		metric.WithDescription("Total policy evaluation errors"))
	if err != nil {
		return Counters{}, fmt.Errorf("otel: counter %s.errors: %w", subsystem, err)
	}

	return Counters{
		Evaluations: eval,
		Mutations:   mut,
		Denials:     den,
		Expansions:  exp,
		Errors:      errC,
	}, nil
}

// Histograms holds the standard histograms used by multiple packages.
type Histograms struct {
	EvalDuration metric.Float64Histogram
}

// NewHistograms constructs a Histograms set registered under the given subsystem name.
func NewHistograms(subsystem string) (Histograms, error) {
	m := Meter(subsystem)
	prefix := Namespace + "." + subsystem

	dur, err := m.Float64Histogram(prefix+".eval_duration_ms",
		metric.WithDescription("Policy evaluation latency in milliseconds"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 5, 10, 25, 50, 100, 250, 500),
	)
	if err != nil {
		return Histograms{}, fmt.Errorf("otel: histogram %s.eval_duration: %w", subsystem, err)
	}
	return Histograms{EvalDuration: dur}, nil
}
