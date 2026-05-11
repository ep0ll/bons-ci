package dirsync

import (
	"sync"
)

// defaultBufSize is 64 KiB — aligned with typical kernel read-ahead windows
// and sized to fit comfortably within L2 cache on modern CPUs, minimising
// system-call frequency without blowing the data cache between calls.
const defaultBufSize = 64 << 10 // 64 KiB

// ─────────────────────────────────────────────────────────────────────────────
// BufPool — reusable fixed-size byte slices
// ─────────────────────────────────────────────────────────────────────────────

// BufPool is a concurrency-safe pool of fixed-size byte slices.
//
// Pooling eliminates the per-file heap allocation that would otherwise occur
// on the hot comparison path. When comparing thousands of files each 64 KiB
// allocation independently pressures the GC; pooling amortises that cost to
// near zero by reusing the same physical memory across calls.
//
// Callers must return slices via [BufPool.Put] when finished. Holding a buffer
// longer than necessary prevents it from being reused by concurrent goroutines.
type BufPool struct {
	pool sync.Pool
}

// NewBufPool creates a [BufPool] whose buffers have the given capacity.
// Values ≤ 0 default to [defaultBufSize] (64 KiB).
func NewBufPool(size int) *BufPool {
	if size <= 0 {
		size = defaultBufSize
	}
	return &BufPool{
		pool: sync.Pool{
			New: func() any {
				b := make([]byte, size)
				return &b
			},
		},
	}
}

// Get returns a buffer from the pool. Call [BufPool.Put] when done.
func (p *BufPool) Get() *[]byte { return p.pool.Get().(*[]byte) }

// Put returns buf to the pool. The buffer must not be used after this call.
func (p *BufPool) Put(buf *[]byte) { p.pool.Put(buf) }

// ─────────────────────────────────────────────────────────────────────────────
// Package-level shared pools
// ─────────────────────────────────────────────────────────────────────────────

// sharedBufPool is the default buffer pool shared across all [TwoPhaseHasher]
// instances that do not supply their own pool. Declared here (not in hasher.go)
// so every internal consumer references one pool rather than maintaining
// independent ones — maximising reuse and minimising GC pressure.
var sharedBufPool = NewBufPool(defaultBufSize)
