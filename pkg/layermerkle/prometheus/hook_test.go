package prometheus_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bons/bons-ci/pkg/layermerkle"
	lmprom "github.com/bons/bons-ci/pkg/layermerkle/prometheus"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

func TestHook_CountersAccumulate(t *testing.T) {
	h := lmprom.NewHook()
	ctx := context.Background()

	req := layermerkle.HashRequest{RelPath: "usr/bin/sh"}
	ev := testutil.NewAccessEvent().Build()
	tree := &layermerkle.MerkleTree{LeafCount: 10}

	h.OnCacheHit(ctx, req, &layermerkle.HashResult{})
	h.OnCacheHit(ctx, req, &layermerkle.HashResult{})
	h.OnHashStart(ctx, req)
	h.OnHashError(ctx, req, nil)
	h.OnEventDropped(ctx, ev, nil)
	h.OnVertexFinalized(ctx, tree)

	s := h.Snapshot()
	if s.CacheHits != 2 {
		t.Errorf("CacheHits = %d, want 2", s.CacheHits)
	}
	if s.HashTotal != 1 {
		t.Errorf("HashTotal = %d, want 1", s.HashTotal)
	}
	if s.HashErrors != 1 {
		t.Errorf("HashErrors = %d, want 1", s.HashErrors)
	}
	if s.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", s.Dropped)
	}
	if s.Finalized != 1 {
		t.Errorf("Finalized = %d, want 1", s.Finalized)
	}
	if s.LeavesTotal != 10 {
		t.Errorf("LeavesTotal = %d, want 10", s.LeavesTotal)
	}
}

func TestHook_Reset_ZerosCounters(t *testing.T) {
	h := lmprom.NewHook()
	h.OnHashStart(context.Background(), layermerkle.HashRequest{})
	h.Reset()
	if h.Snapshot().HashTotal != 0 {
		t.Error("Reset should zero all counters")
	}
}

func TestHook_ServeHTTP_PrometheusFormat(t *testing.T) {
	h := lmprom.NewHook()
	h.OnCacheHit(context.Background(), layermerkle.HashRequest{}, &layermerkle.HashResult{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	text := string(body)

	requiredLines := []string{
		"# HELP layermerkle_cache_hits_total",
		"# TYPE layermerkle_cache_hits_total counter",
		"layermerkle_cache_hits_total 1",
		"layermerkle_cache_misses_total 0",
		"layermerkle_hash_computations_total 0",
	}
	for _, line := range requiredLines {
		if !strings.Contains(text, line) {
			t.Errorf("metrics output missing line: %q", line)
		}
	}
}

func TestHook_ImplementsEngineHook_Interface(t *testing.T) {
	var _ layermerkle.EngineHook = lmprom.NewHook()
}

func TestHook_ImplementsHTTPHandler_Interface(t *testing.T) {
	var _ http.Handler = lmprom.NewHook()
}
