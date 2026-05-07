package otel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bons/bons-ci/pkg/layermerkle"
	lmotel "github.com/bons/bons-ci/pkg/layermerkle/otel"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Spy implementations for test assertions
// ─────────────────────────────────────────────────────────────────────────────

type spySpan struct {
	ended      bool
	err        error
	attributes map[string]any
}

func newSpySpan() *spySpan {
	return &spySpan{attributes: make(map[string]any)}
}

func (s *spySpan) End()                         { s.ended = true }
func (s *spySpan) RecordError(err error)        { s.err = err }
func (s *spySpan) SetAttribute(k string, v any) { s.attributes[k] = v }

type spyTracer struct {
	spans []*spySpan
}

func (t *spyTracer) Start(ctx context.Context, _ string) (context.Context, lmotel.Span) {
	s := newSpySpan()
	t.spans = append(t.spans, s)
	return ctx, s
}

func (t *spyTracer) lastSpan() *spySpan {
	if len(t.spans) == 0 {
		return nil
	}
	return t.spans[len(t.spans)-1]
}

type spyCounter struct{ count int64 }

func (c *spyCounter) Add(_ context.Context, n int64, _ ...lmotel.KV) { c.count += n }

type spyHistogram struct{ recorded []float64 }

func (h *spyHistogram) Record(_ context.Context, v float64, _ ...lmotel.KV) {
	h.recorded = append(h.recorded, v)
}

type spyMeter struct {
	hits *spyCounter
	miss *spyCounter
	dur  *spyHistogram
	leaf *spyCounter
}

func newSpyMeter() *spyMeter {
	return &spyMeter{
		hits: &spyCounter{},
		miss: &spyCounter{},
		dur:  &spyHistogram{},
		leaf: &spyCounter{},
	}
}

func (m *spyMeter) Int64Counter(name, _ string) (lmotel.Counter, error) {
	switch name {
	case "layermerkle.cache.hits":
		return m.hits, nil
	case "layermerkle.cache.misses":
		return m.miss, nil
	case "layermerkle.vertex.leaves":
		return m.leaf, nil
	default:
		return &spyCounter{}, nil
	}
}

func (m *spyMeter) Float64Histogram(_, _ string) (lmotel.Histogram, error) {
	return m.dur, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestOTELHook_OnCacheHit_IncrementsCounter(t *testing.T) {
	meter := newSpyMeter()
	hook, err := lmotel.NewHook(lmotel.NoopTracer{}, meter)
	if err != nil {
		t.Fatalf("NewHook: %v", err)
	}

	ctx := context.Background()
	req := layermerkle.HashRequest{RelPath: "lib/x"}
	hook.OnCacheHit(ctx, req, &layermerkle.HashResult{})
	hook.OnCacheHit(ctx, req, &layermerkle.HashResult{})

	if meter.hits.count != 2 {
		t.Errorf("cache hits counter = %d, want 2", meter.hits.count)
	}
}

func TestOTELHook_OnHashStart_OpenSpan(t *testing.T) {
	tracer := &spyTracer{}
	hook, _ := lmotel.NewHook(tracer, lmotel.NoopMeter{})

	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v1"),
		LayerStack: testutil.MakeLayerStack("l1"),
		RelPath:    "usr/bin/python3",
	}
	hook.OnHashStart(context.Background(), req)

	if len(tracer.spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(tracer.spans))
	}
	span := tracer.lastSpan()
	if span.ended {
		t.Error("span should not be ended after OnHashStart")
	}
	if span.attributes["layermerkle.rel.path"] != "usr/bin/python3" {
		t.Errorf("span missing rel.path attribute, got %v", span.attributes)
	}
}

func TestOTELHook_OnHashComplete_EndsSpanAndRecordsLatency(t *testing.T) {
	tracer := &spyTracer{}
	meter := newSpyMeter()
	hook, _ := lmotel.NewHook(tracer, meter)

	req := layermerkle.HashRequest{
		VertexID: testutil.MakeVertexID("v2"),
		RelPath:  "bin/sh",
	}
	hash := testutil.MakeFileHash("content")

	hook.OnHashStart(context.Background(), req)
	hook.OnHashComplete(context.Background(), req, hash)

	span := tracer.lastSpan()
	if !span.ended {
		t.Error("span should be ended after OnHashComplete")
	}
	if len(meter.dur.recorded) != 1 {
		t.Errorf("histogram records = %d, want 1", len(meter.dur.recorded))
	}
}

func TestOTELHook_OnHashError_RecordsErrorOnSpan(t *testing.T) {
	tracer := &spyTracer{}
	hook, _ := lmotel.NewHook(tracer, lmotel.NoopMeter{})

	req := layermerkle.HashRequest{RelPath: "etc/shadow"}
	sentinel := errors.New("disk read failed")

	hook.OnHashStart(context.Background(), req)
	hook.OnHashError(context.Background(), req, sentinel)

	span := tracer.lastSpan()
	if span.err == nil {
		t.Error("error should be recorded on span")
	}
	if !span.ended {
		t.Error("span should be ended after OnHashError")
	}
}

func TestOTELHook_OnVertexFinalized_RecordsLeafCount(t *testing.T) {
	meter := newSpyMeter()
	hook, _ := lmotel.NewHook(lmotel.NoopTracer{}, meter)

	tree := &layermerkle.MerkleTree{
		VertexID:  testutil.MakeVertexID("v3"),
		LeafCount: 42,
	}
	hook.OnVertexFinalized(context.Background(), tree)

	if meter.leaf.count != 42 {
		t.Errorf("leaves counter = %d, want 42", meter.leaf.count)
	}
}

func TestOTELHook_NoopMeter_NoError(t *testing.T) {
	hook, err := lmotel.NewHook(lmotel.NoopTracer{}, lmotel.NoopMeter{})
	if err != nil {
		t.Fatalf("NewHook with noops should not error: %v", err)
	}
	if hook == nil {
		t.Fatal("hook should not be nil")
	}
}

func TestOTELHook_NilMeter_NoError(t *testing.T) {
	hook, err := lmotel.NewHook(nil, nil)
	if err != nil {
		t.Fatalf("NewHook with nil meter/tracer should not error: %v", err)
	}
	// All methods should be safe to call on a nil-instrument hook.
	ctx := context.Background()
	req := layermerkle.HashRequest{RelPath: "a"}
	hook.OnCacheHit(ctx, req, &layermerkle.HashResult{})
	hook.OnHashStart(ctx, req)
	hook.OnHashComplete(ctx, req, testutil.MakeFileHash("x"))
	hook.OnHashError(ctx, req, errors.New("e"))
	hook.OnVertexFinalized(ctx, &layermerkle.MerkleTree{})
}
