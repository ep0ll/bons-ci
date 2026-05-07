// Package otel provides an OpenTelemetry-instrumented EngineHook for the
// layermerkle engine.
//
// The hook records:
//   - A span per file hash computation (layermerkle.hash)
//   - Cache hit / miss counters (layermerkle.cache.hits, layermerkle.cache.misses)
//   - Hash latency histogram (layermerkle.hash.duration_ms)
//   - Vertex finalization events with leaf count and cache rate
//
// No core layermerkle package imports this sub-package. Wire it at call sites:
//
//	hook := otel.NewHook(tracer, meter)
//	engine := layermerkle.NewEngine(layermerkle.WithHook(hook))
//
// The hook uses the same thin interface approach as fanwatch/middleware/otel.go:
// abstract Tracer and Meter interfaces so the package compiles without any
// specific OTEL SDK and can be tested with noops.
package otel

import (
	"context"
	"fmt"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
)

// ─────────────────────────────────────────────────────────────────────────────
// Thin OTEL abstractions — no SDK import
// ─────────────────────────────────────────────────────────────────────────────

// Span is the subset of trace.Span used by the hook.
type Span interface {
	End()
	RecordError(err error)
	SetAttribute(key string, value any)
}

// Tracer creates spans.
type Tracer interface {
	Start(ctx context.Context, name string) (context.Context, Span)
}

// Counter is an int64 monotonic counter.
type Counter interface {
	Add(ctx context.Context, n int64, attrs ...KV)
}

// Histogram records a distribution of values.
type Histogram interface {
	Record(ctx context.Context, value float64, attrs ...KV)
}

// Meter creates metric instruments.
type Meter interface {
	Int64Counter(name, description string) (Counter, error)
	Float64Histogram(name, description string) (Histogram, error)
}

// KV is a key-value attribute.
type KV struct{ Key string; Value any }

// Attr constructs a KV.
func Attr(k string, v any) KV { return KV{Key: k, Value: v} }

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// Config configures the OTELHook.
type Config struct {
	// Tracer creates per-hash spans. Nil disables tracing.
	Tracer Tracer

	// Meter creates metric instruments. Nil disables metrics.
	Meter Meter

	// RecordPaths controls whether file paths are recorded on spans.
	// Disable in high-cardinality production environments.
	RecordPaths bool
}

// ─────────────────────────────────────────────────────────────────────────────
// OTELHook
// ─────────────────────────────────────────────────────────────────────────────

// OTELHook is a layermerkle.EngineHook that emits OpenTelemetry signals.
type OTELHook struct {
	layermerkle.NoopHook
	cfg       Config
	cacheHits Counter
	cacheMiss Counter
	hashDur   Histogram
	leaves    Counter
	spans     map[string]spanEntry // keyed by sfKey — best-effort
}

type spanEntry struct {
	span  Span
	start time.Time
}

// NewHook constructs an OTELHook from abstract tracer and meter.
func NewHook(tracer Tracer, meter Meter) (*OTELHook, error) {
	return NewHookWithConfig(Config{Tracer: tracer, Meter: meter, RecordPaths: true})
}

// NewHookWithConfig constructs an OTELHook with explicit config.
func NewHookWithConfig(cfg Config) (*OTELHook, error) {
	h := &OTELHook{cfg: cfg, spans: make(map[string]spanEntry)}
	if cfg.Meter == nil {
		return h, nil
	}
	var err error
	if h.cacheHits, err = cfg.Meter.Int64Counter(
		"layermerkle.cache.hits", "File hash cache hits",
	); err != nil {
		return nil, fmt.Errorf("otel hook: cache hits counter: %w", err)
	}
	if h.cacheMiss, err = cfg.Meter.Int64Counter(
		"layermerkle.cache.misses", "File hash cache misses",
	); err != nil {
		return nil, fmt.Errorf("otel hook: cache misses counter: %w", err)
	}
	if h.hashDur, err = cfg.Meter.Float64Histogram(
		"layermerkle.hash.duration_ms", "File hash computation latency in milliseconds",
	); err != nil {
		return nil, fmt.Errorf("otel hook: hash duration histogram: %w", err)
	}
	if h.leaves, err = cfg.Meter.Int64Counter(
		"layermerkle.vertex.leaves", "Total Merkle tree leaves across all vertices",
	); err != nil {
		return nil, fmt.Errorf("otel hook: leaves counter: %w", err)
	}
	return h, nil
}

// OnCacheHit implements EngineHook.
func (h *OTELHook) OnCacheHit(ctx context.Context, req layermerkle.HashRequest, _ *layermerkle.HashResult) {
	if h.cacheHits == nil {
		return
	}
	attrs := h.pathAttrs(req)
	h.cacheHits.Add(ctx, 1, attrs...)
}

// OnHashStart implements EngineHook — opens a span for the hash computation.
func (h *OTELHook) OnHashStart(ctx context.Context, req layermerkle.HashRequest) {
	if h.cacheMiss != nil {
		h.cacheMiss.Add(ctx, 1, h.pathAttrs(req)...)
	}
	if h.cfg.Tracer == nil {
		return
	}
	_, span := h.cfg.Tracer.Start(ctx, "layermerkle.hash")
	if h.cfg.RecordPaths {
		span.SetAttribute("layermerkle.rel.path", req.RelPath)
		span.SetAttribute("layermerkle.layer.stack", string(layermerkle.NewLayerStackKey(req.LayerStack)))
	}
	h.spans[spanKey(req)] = spanEntry{span: span, start: time.Now()}
}

// OnHashComplete implements EngineHook — closes the span and records latency.
func (h *OTELHook) OnHashComplete(ctx context.Context, req layermerkle.HashRequest, hash layermerkle.FileHash) {
	key := spanKey(req)
	if entry, ok := h.spans[key]; ok {
		elapsed := time.Since(entry.start).Milliseconds()
		entry.span.SetAttribute("layermerkle.hash.result", string(hash))
		entry.span.End()
		delete(h.spans, key)
		if h.hashDur != nil {
			h.hashDur.Record(ctx, float64(elapsed), h.pathAttrs(req)...)
		}
	}
}

// OnHashError implements EngineHook — records the error on the span.
func (h *OTELHook) OnHashError(ctx context.Context, req layermerkle.HashRequest, err error) {
	key := spanKey(req)
	if entry, ok := h.spans[key]; ok {
		entry.span.RecordError(err)
		entry.span.End()
		delete(h.spans, key)
	}
}

// OnVertexFinalized implements EngineHook — records leaf count.
func (h *OTELHook) OnVertexFinalized(ctx context.Context, tree *layermerkle.MerkleTree) {
	if h.leaves != nil {
		h.leaves.Add(ctx, int64(tree.LeafCount),
			Attr("layermerkle.vertex.id", string(tree.VertexID)),
		)
	}
}

func (h *OTELHook) pathAttrs(req layermerkle.HashRequest) []KV {
	if !h.cfg.RecordPaths {
		return nil
	}
	return []KV{
		Attr("layermerkle.rel.path", req.RelPath),
		Attr("layermerkle.vertex.id", string(req.VertexID)),
	}
}

func spanKey(req layermerkle.HashRequest) string {
	return string(req.VertexID) + "\x00" + req.RelPath
}

// ─────────────────────────────────────────────────────────────────────────────
// Noop implementations for testing
// ─────────────────────────────────────────────────────────────────────────────

// NoopTracer is a do-nothing Tracer.
type NoopTracer struct{}

func (NoopTracer) Start(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) End()                      {}
func (noopSpan) RecordError(_ error)       {}
func (noopSpan) SetAttribute(_ string, _ any) {}

// NoopMeter is a do-nothing Meter.
type NoopMeter struct{}

func (NoopMeter) Int64Counter(_, _ string) (Counter, error)       { return noopCounter{}, nil }
func (NoopMeter) Float64Histogram(_, _ string) (Histogram, error) { return noopHistogram{}, nil }

type noopCounter struct{}

func (noopCounter) Add(_ context.Context, _ int64, _ ...KV) {}

type noopHistogram struct{}

func (noopHistogram) Record(_ context.Context, _ float64, _ ...KV) {}
