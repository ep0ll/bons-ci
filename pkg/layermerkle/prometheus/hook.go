// Package prometheus provides a Prometheus-compatible EngineHook for the
// layermerkle engine using only the Go standard library.
//
// It exposes metrics in the Prometheus text exposition format via an
// http.Handler that can be registered with any HTTP mux:
//
//	hook := prometheus.NewHook()
//	engine := layermerkle.NewEngine(layermerkle.WithHook(hook))
//	http.Handle("/metrics", hook)
//
// Metrics exposed:
//
//	layermerkle_cache_hits_total            counter
//	layermerkle_cache_misses_total          counter
//	layermerkle_hash_computations_total     counter
//	layermerkle_hash_errors_total           counter
//	layermerkle_events_dropped_total        counter
//	layermerkle_vertices_finalized_total    counter
//	layermerkle_vertex_leaves_total         counter
package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/bons/bons-ci/pkg/layermerkle"
)

// ─────────────────────────────────────────────────────────────────────────────
// Hook
// ─────────────────────────────────────────────────────────────────────────────

// Hook is a layermerkle.EngineHook that exposes Prometheus-format metrics.
type Hook struct {
	layermerkle.NoopHook
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
	hashTotal   atomic.Int64
	hashErrors  atomic.Int64
	dropped     atomic.Int64
	finalized   atomic.Int64
	leavesTotal atomic.Int64
}

// NewHook returns an initialized Hook.
func NewHook() *Hook { return &Hook{} }

// OnCacheHit implements layermerkle.EngineHook.
func (h *Hook) OnCacheHit(_ context.Context, _ layermerkle.HashRequest, _ *layermerkle.HashResult) {
	h.cacheHits.Add(1)
}

// OnHashStart implements layermerkle.EngineHook.
func (h *Hook) OnHashStart(_ context.Context, _ layermerkle.HashRequest) {
	h.cacheMisses.Add(1)
	h.hashTotal.Add(1)
}

// OnHashError implements layermerkle.EngineHook.
func (h *Hook) OnHashError(_ context.Context, _ layermerkle.HashRequest, _ error) {
	h.hashErrors.Add(1)
}

// OnEventDropped implements layermerkle.EngineHook.
func (h *Hook) OnEventDropped(_ context.Context, _ *layermerkle.AccessEvent, _ error) {
	h.dropped.Add(1)
}

// OnVertexFinalized implements layermerkle.EngineHook.
func (h *Hook) OnVertexFinalized(_ context.Context, tree *layermerkle.MerkleTree) {
	h.finalized.Add(1)
	h.leavesTotal.Add(int64(tree.LeafCount))
}

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot — point-in-time counter view
// ─────────────────────────────────────────────────────────────────────────────

// Snapshot is an immutable point-in-time view of all counters.
type Snapshot struct {
	CacheHits   int64
	CacheMisses int64
	HashTotal   int64
	HashErrors  int64
	Dropped     int64
	Finalized   int64
	LeavesTotal int64
}

// Snapshot returns the current counter values.
func (h *Hook) Snapshot() Snapshot {
	return Snapshot{
		CacheHits:   h.cacheHits.Load(),
		CacheMisses: h.cacheMisses.Load(),
		HashTotal:   h.hashTotal.Load(),
		HashErrors:  h.hashErrors.Load(),
		Dropped:     h.dropped.Load(),
		Finalized:   h.finalized.Load(),
		LeavesTotal: h.leavesTotal.Load(),
	}
}

// Reset zeroes all counters. Useful for test isolation.
func (h *Hook) Reset() {
	h.cacheHits.Store(0)
	h.cacheMisses.Store(0)
	h.hashTotal.Store(0)
	h.hashErrors.Store(0)
	h.dropped.Store(0)
	h.finalized.Store(0)
	h.leavesTotal.Store(0)
}

// ServeHTTP serves Prometheus text exposition format metrics.
// Register with: http.Handle("/metrics", hook)
func (h *Hook) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s := h.Snapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	metrics := []struct {
		name  string
		help  string
		kind  string
		value int64
	}{
		{"layermerkle_cache_hits_total", "Total file hash cache hits.", "counter", s.CacheHits},
		{"layermerkle_cache_misses_total", "Total file hash cache misses.", "counter", s.CacheMisses},
		{"layermerkle_hash_computations_total", "Total file hashing operations started.", "counter", s.HashTotal},
		{"layermerkle_hash_errors_total", "Total file hashing errors.", "counter", s.HashErrors},
		{"layermerkle_events_dropped_total", "Total access events dropped.", "counter", s.Dropped},
		{"layermerkle_vertices_finalized_total", "Total ExecOp vertices finalized.", "counter", s.Finalized},
		{"layermerkle_vertex_leaves_total", "Total Merkle tree leaf nodes across all vertices.", "counter", s.LeavesTotal},
	}
	for _, m := range metrics {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n",
			m.name, m.help, m.name, m.kind, m.name, m.value,
		)
	}
}
