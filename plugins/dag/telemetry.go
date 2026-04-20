package reactdag

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Tracer / Span / Counter / Histogram — OTel-shaped interfaces, zero external deps
//
// Production code satisfies these with real go.opentelemetry.io/otel types
// (which implement identical method signatures). The package itself is pure stdlib.
// ---------------------------------------------------------------------------

// Span is a minimal interface satisfied by both otel trace.Span and noopSpan.
type Span interface {
	End()
	SetAttribute(key string, value interface{})
	RecordError(err error)
}

// Tracer creates Spans.
type Tracer interface {
	Start(ctx context.Context, name string, attrs map[string]interface{}) (context.Context, Span)
}

// Counter is a monotonically increasing metric instrument.
type Counter interface {
	Add(ctx context.Context, delta int64, attrs map[string]interface{})
}

// Histogram records a distribution of observed values.
type Histogram interface {
	Record(ctx context.Context, value float64, attrs map[string]interface{})
}

// ---------------------------------------------------------------------------
// Telemetry — composes all instruments into a single scheduler façade
// ---------------------------------------------------------------------------

// Telemetry is the scheduler's single observability façade.
// Construct with NewTelemetry (real instruments) or NewNoopTelemetry (tests).
type Telemetry struct {
	tracer         Tracer
	vertexDuration Histogram
	buildDuration  Histogram
	cacheHits      Counter
	cacheMisses    Counter
	filesRead      Counter
	filesWritten   Counter
	invalidations  Counter
}

// NewTelemetry constructs a Telemetry from caller-supplied instruments.
// Any nil instrument is replaced by a no-op equivalent.
func NewTelemetry(
	tracer Tracer,
	vertexDuration Histogram,
	buildDuration Histogram,
	cacheHits Counter,
	cacheMisses Counter,
	filesRead Counter,
	filesWritten Counter,
	invalidations Counter,
) *Telemetry {
	return &Telemetry{
		tracer:         orTracer(tracer),
		vertexDuration: orHistogram(vertexDuration),
		buildDuration:  orHistogram(buildDuration),
		cacheHits:      orCounter(cacheHits),
		cacheMisses:    orCounter(cacheMisses),
		filesRead:      orCounter(filesRead),
		filesWritten:   orCounter(filesWritten),
		invalidations:  orCounter(invalidations),
	}
}

// NewNoopTelemetry returns a Telemetry that discards all observations.
func NewNoopTelemetry() *Telemetry {
	return NewTelemetry(nil, nil, nil, nil, nil, nil, nil, nil)
}

// ---------------------------------------------------------------------------
// Telemetry methods — called by the Scheduler
// ---------------------------------------------------------------------------

// StartVertexSpan opens a tracing span for a single vertex execution.
func (t *Telemetry) StartVertexSpan(ctx context.Context, v *Vertex) (context.Context, Span) {
	return t.tracer.Start(ctx, "dag.vertex.execute", map[string]interface{}{
		"dag.vertex.id": v.ID(),
		"dag.vertex.op": v.OpID(),
	})
}

// StartBuildSpan opens a tracing span covering an entire build.
func (t *Telemetry) StartBuildSpan(ctx context.Context, targetID string) (context.Context, Span) {
	return t.tracer.Start(ctx, "dag.build", map[string]interface{}{
		"dag.build.target": targetID,
	})
}

// RecordCacheResult increments the hit or miss counter for the given tier.
func (t *Telemetry) RecordCacheResult(ctx context.Context, v *Vertex, tier string, hit bool) {
	attrs := map[string]interface{}{
		"dag.vertex.id":  v.ID(),
		"dag.cache.tier": tier,
	}
	if hit {
		t.cacheHits.Add(ctx, 1, attrs)
	} else {
		t.cacheMisses.Add(ctx, 1, attrs)
	}
}

// RecordVertexDuration records vertex wall-clock time in milliseconds.
func (t *Telemetry) RecordVertexDuration(ctx context.Context, v *Vertex, dur time.Duration) {
	t.vertexDuration.Record(ctx, float64(dur.Milliseconds()), map[string]interface{}{
		"dag.vertex.id": v.ID(),
		"dag.vertex.op": v.OpID(),
	})
}

// RecordFileAccess increments file read/write counters for a vertex.
func (t *Telemetry) RecordFileAccess(ctx context.Context, v *Vertex, read, written int) {
	attrs := map[string]interface{}{"dag.vertex.id": v.ID()}
	t.filesRead.Add(ctx, int64(read), attrs)
	t.filesWritten.Add(ctx, int64(written), attrs)
}

// RecordBuildDuration records total build time in milliseconds.
func (t *Telemetry) RecordBuildDuration(ctx context.Context, targetID string, dur time.Duration) {
	t.buildDuration.Record(ctx, float64(dur.Milliseconds()), map[string]interface{}{
		"dag.build.target": targetID,
	})
}

// RecordInvalidation increments the invalidation counter.
func (t *Telemetry) RecordInvalidation(ctx context.Context, count int) {
	t.invalidations.Add(ctx, int64(count), nil)
}

// ---------------------------------------------------------------------------
// No-op implementations — zero allocation, zero overhead
// ---------------------------------------------------------------------------

type noopSpan struct{}

func (noopSpan) End()                                {}
func (noopSpan) SetAttribute(_ string, _ interface{}) {}
func (noopSpan) RecordError(_ error)                  {}

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ map[string]interface{}) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopCounter struct{}

func (noopCounter) Add(_ context.Context, _ int64, _ map[string]interface{}) {}

type noopHistogram struct{}

func (noopHistogram) Record(_ context.Context, _ float64, _ map[string]interface{}) {}

// ---------------------------------------------------------------------------
// Nil-safe fallbacks
// ---------------------------------------------------------------------------

func orTracer(t Tracer) Tracer {
	if t == nil {
		return noopTracer{}
	}
	return t
}

func orCounter(c Counter) Counter {
	if c == nil {
		return noopCounter{}
	}
	return c
}

func orHistogram(h Histogram) Histogram {
	if h == nil {
		return noopHistogram{}
	}
	return h
}
