// Package hook — MetricsHook provides a zero-dependency structured metrics
// collector that can be exposed to Prometheus, OpenTelemetry, or any other
// metrics system via the Snapshot method.
package hook

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// MetricsSnapshot
// ─────────────────────────────────────────────────────────────────────────────

// MetricsSnapshot is an immutable point-in-time view of all collected metrics.
type MetricsSnapshot struct {
	// Cache metrics.
	CacheHits   int64
	CacheMisses int64

	// Hash computation metrics.
	HashComputations int64
	HashTotalNs      int64 // cumulative nanoseconds spent in HashProvider.Hash

	// Tombstone metrics.
	Tombstones int64

	// Merkle metrics.
	MerkleLeafsAdded int64
	LayersSealed     int64

	// Error metrics.
	Errors int64

	// Pipeline lifecycle.
	PipelineStarts int64
	PipelineStops  int64

	// Time of snapshot.
	Timestamp time.Time
}

// HitRate returns the cache hit rate as a fraction in [0, 1].
func (s MetricsSnapshot) HitRate() float64 {
	total := s.CacheHits + s.CacheMisses
	if total == 0 {
		return 0
	}
	return float64(s.CacheHits) / float64(total)
}

// AvgHashLatencyMs returns the average hash computation latency in milliseconds.
func (s MetricsSnapshot) AvgHashLatencyMs() float64 {
	if s.HashComputations == 0 {
		return 0
	}
	return float64(s.HashTotalNs) / float64(s.HashComputations) / 1e6
}

// String returns a human-readable one-line summary.
func (s MetricsSnapshot) String() string {
	return fmt.Sprintf(
		"cache(hit=%d miss=%d rate=%.1f%%) hash(n=%d avg=%.2fms) tombstones=%d leaves=%d layers=%d errors=%d",
		s.CacheHits, s.CacheMisses, s.HitRate()*100,
		s.HashComputations, s.AvgHashLatencyMs(),
		s.Tombstones, s.MerkleLeafsAdded, s.LayersSealed, s.Errors,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// MetricsHook
// ─────────────────────────────────────────────────────────────────────────────

// MetricsHook collects structured pipeline metrics using atomic counters.
// It implements Hook and is safe for concurrent use from any goroutine.
//
// Usage:
//
//	m := hook.NewMetricsHook()
//	p, _ := pipeline.New(pipeline.WithHook(m), ...)
//	// ...later...
//	snap := m.Snapshot()
//	fmt.Println(snap)
type MetricsHook struct {
	cacheHits    atomic.Int64
	cacheMisses  atomic.Int64
	hashComps    atomic.Int64
	hashTotalNs  atomic.Int64
	tombstones   atomic.Int64
	leavesAdded  atomic.Int64
	layersSealed atomic.Int64
	errors       atomic.Int64
	pipeStarts   atomic.Int64
	pipeStops    atomic.Int64
}

// NewMetricsHook creates a zeroed MetricsHook.
func NewMetricsHook() *MetricsHook { return &MetricsHook{} }

// OnHook implements Hook.
func (m *MetricsHook) OnHook(_ context.Context, e HookEvent) error {
	switch e.Type {
	case HookCacheHit:
		m.cacheHits.Add(1)
	case HookCacheMiss:
		m.cacheMisses.Add(1)
	case HookHashComputed:
		m.hashComps.Add(1)
		if e.Event != nil && !e.Timestamp.IsZero() {
			// Approximate latency from event timestamp to now.
			ns := time.Since(e.Event.Timestamp).Nanoseconds()
			if ns > 0 {
				m.hashTotalNs.Add(ns)
			}
		}
	case HookTombstone:
		m.tombstones.Add(1)
	case HookMerkleLeafAdded:
		m.leavesAdded.Add(1)
	case HookLayerSealed:
		m.layersSealed.Add(1)
	case HookError:
		m.errors.Add(1)
	case HookPipelineStarted:
		m.pipeStarts.Add(1)
	case HookPipelineStopped:
		m.pipeStops.Add(1)
	}
	return nil
}

// Snapshot returns an immutable point-in-time view of all counters.
func (m *MetricsHook) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		CacheHits:        m.cacheHits.Load(),
		CacheMisses:      m.cacheMisses.Load(),
		HashComputations: m.hashComps.Load(),
		HashTotalNs:      m.hashTotalNs.Load(),
		Tombstones:       m.tombstones.Load(),
		MerkleLeafsAdded: m.leavesAdded.Load(),
		LayersSealed:     m.layersSealed.Load(),
		Errors:           m.errors.Load(),
		PipelineStarts:   m.pipeStarts.Load(),
		PipelineStops:    m.pipeStops.Load(),
		Timestamp:        time.Now(),
	}
}

// Reset zeroes all counters. Useful for per-ExecOp metric windows.
func (m *MetricsHook) Reset() {
	m.cacheHits.Store(0)
	m.cacheMisses.Store(0)
	m.hashComps.Store(0)
	m.hashTotalNs.Store(0)
	m.tombstones.Store(0)
	m.leavesAdded.Store(0)
	m.layersSealed.Store(0)
	m.errors.Store(0)
	m.pipeStarts.Store(0)
	m.pipeStops.Store(0)
}

// Compile-time assertion.
var _ Hook = (*MetricsHook)(nil)
