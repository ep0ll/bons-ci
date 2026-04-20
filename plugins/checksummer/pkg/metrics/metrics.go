// Package metrics provides lock-free, low-overhead performance recording.
// All counters use sync/atomic so recording never blocks the hot path.
package metrics

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"
)

// Counter is a monotonically increasing lock-free integer.
type Counter struct{ v int64 }

func (c *Counter) Inc()        { atomic.AddInt64(&c.v, 1) }
func (c *Counter) Add(n int64) { atomic.AddInt64(&c.v, n) }
func (c *Counter) Load() int64 { return atomic.LoadInt64(&c.v) }
func (c *Counter) Reset()      { atomic.StoreInt64(&c.v, 0) }

// Histogram records latency distributions using power-of-two µs buckets.
// 32 buckets cover 1 µs through ~4 s.
type Histogram struct {
	buckets [32]int64
	count   int64
	sum     int64 // µs
	min     int64 // µs (0 = never recorded)
	max     int64 // µs
}

// Record adds a duration observation.
func (h *Histogram) Record(d time.Duration) {
	us := d.Microseconds()
	if us < 0 {
		us = 0
	}
	atomic.AddInt64(&h.count, 1)
	atomic.AddInt64(&h.sum, us)

	for {
		cur := atomic.LoadInt64(&h.min)
		if cur != 0 && cur <= us {
			break
		}
		if atomic.CompareAndSwapInt64(&h.min, cur, us) {
			break
		}
	}
	for {
		cur := atomic.LoadInt64(&h.max)
		if cur >= us {
			break
		}
		if atomic.CompareAndSwapInt64(&h.max, cur, us) {
			break
		}
	}
	idx := 0
	if us > 0 {
		idx = int(math.Log2(float64(us)))
		if idx >= 32 {
			idx = 31
		}
	}
	atomic.AddInt64(&h.buckets[idx], 1)
}

// HistSnapshot is a point-in-time copy of a Histogram.
type HistSnapshot struct {
	Count   int64
	SumUS   int64
	MinUS   int64
	MaxUS   int64
	Buckets [32]int64
}

// Snapshot captures current state.
func (h *Histogram) Snapshot() HistSnapshot {
	var s HistSnapshot
	s.Count = atomic.LoadInt64(&h.count)
	s.SumUS = atomic.LoadInt64(&h.sum)
	s.MinUS = atomic.LoadInt64(&h.min)
	s.MaxUS = atomic.LoadInt64(&h.max)
	for i := range s.Buckets {
		s.Buckets[i] = atomic.LoadInt64(&h.buckets[i])
	}
	return s
}

func (s HistSnapshot) MeanUS() float64 {
	if s.Count == 0 {
		return 0
	}
	return float64(s.SumUS) / float64(s.Count)
}

// Recorder aggregates all engine metrics.  Zero value is ready to use.
type Recorder struct {
	EventsReceived Counter
	EventsFiltered Counter
	EventsDropped  Counter

	HashesComputed Counter
	HashesDeduped  Counter
	BytesHashed    Counter
	HashErrors     Counter

	CacheHits   Counter
	CacheMisses Counter
	CacheEvicts Counter

	ChunksRead     Counter
	ParallelHashes Counter
	MmapHashes     Counter

	LayerResolutions Counter
	LayerFallbacks   Counter

	HashLatency   Histogram
	EventLatency  Histogram
	ChunkReadTime Histogram
}

// Snapshot is an immutable, point-in-time copy of all counters.
type Snapshot struct {
	EventsReceived   int64
	EventsFiltered   int64
	EventsDropped    int64
	HashesComputed   int64
	HashesDeduped    int64
	BytesHashed      int64
	HashErrors       int64
	CacheHits        int64
	CacheMisses      int64
	CacheEvicts      int64
	ChunksRead       int64
	ParallelHashes   int64
	MmapHashes       int64
	LayerResolutions int64
	LayerFallbacks   int64
	HashLatency      HistSnapshot
	EventLatency     HistSnapshot
	ChunkReadTime    HistSnapshot
}

// Snapshot takes a best-effort atomic snapshot of all counters.
func (r *Recorder) Snapshot() Snapshot {
	return Snapshot{
		EventsReceived:   r.EventsReceived.Load(),
		EventsFiltered:   r.EventsFiltered.Load(),
		EventsDropped:    r.EventsDropped.Load(),
		HashesComputed:   r.HashesComputed.Load(),
		HashesDeduped:    r.HashesDeduped.Load(),
		BytesHashed:      r.BytesHashed.Load(),
		HashErrors:       r.HashErrors.Load(),
		CacheHits:        r.CacheHits.Load(),
		CacheMisses:      r.CacheMisses.Load(),
		CacheEvicts:      r.CacheEvicts.Load(),
		ChunksRead:       r.ChunksRead.Load(),
		ParallelHashes:   r.ParallelHashes.Load(),
		MmapHashes:       r.MmapHashes.Load(),
		LayerResolutions: r.LayerResolutions.Load(),
		LayerFallbacks:   r.LayerFallbacks.Load(),
		HashLatency:      r.HashLatency.Snapshot(),
		EventLatency:     r.EventLatency.Snapshot(),
		ChunkReadTime:    r.ChunkReadTime.Snapshot(),
	}
}

func (s Snapshot) String() string {
	return fmt.Sprintf(
		"events=%d filtered=%d dropped=%d | hashes=%d deduped=%d bytes=%d errors=%d | "+
			"cache hits=%d misses=%d evicts=%d | parallel=%d mmap=%d | hash_mean=%.1fµs",
		s.EventsReceived, s.EventsFiltered, s.EventsDropped,
		s.HashesComputed, s.HashesDeduped, s.BytesHashed, s.HashErrors,
		s.CacheHits, s.CacheMisses, s.CacheEvicts,
		s.ParallelHashes, s.MmapHashes,
		s.HashLatency.MeanUS(),
	)
}

// Map converts the snapshot to a string→int64 map for Prometheus / JSON export.
func (s Snapshot) Map() map[string]int64 {
	return map[string]int64{
		"events_received":      s.EventsReceived,
		"events_filtered":      s.EventsFiltered,
		"events_dropped":       s.EventsDropped,
		"hashes_computed":      s.HashesComputed,
		"hashes_deduped":       s.HashesDeduped,
		"bytes_hashed":         s.BytesHashed,
		"hash_errors":          s.HashErrors,
		"cache_hits":           s.CacheHits,
		"cache_misses":         s.CacheMisses,
		"cache_evicts":         s.CacheEvicts,
		"chunks_read":          s.ChunksRead,
		"parallel_hashes":      s.ParallelHashes,
		"mmap_hashes":          s.MmapHashes,
		"layer_resolutions":    s.LayerResolutions,
		"layer_fallbacks":      s.LayerFallbacks,
		"hash_latency_mean_us": int64(s.HashLatency.MeanUS()),
	}
}
