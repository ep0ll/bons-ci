package differ

import (
	"crypto/sha256"
	"hash"
	"sync"
)

// defaultBufSize is 64 KiB — chosen to align with common kernel read-ahead
// window sizes and sit comfortably within L2/L3 cache on modern hardware,
// minimising system-call frequency without blowing the cache.
const defaultBufSize = 64 << 10

// HashFactory constructs a new hash.Hash instance.
//
// Callers may inject any hash implementation here. The default is sha256.New
// (collision-resistant). For pure deduplication workloads where cryptographic
// strength is unnecessary, a faster non-cryptographic factory such as xxhash
// or BLAKE3 can be supplied to significantly reduce CPU time per file.
//
// The factory must be safe to call from concurrent goroutines.
type HashFactory func() hash.Hash

// DefaultHashFactory produces SHA-256 hash instances.
func DefaultHashFactory() hash.Hash { return sha256.New() }

// BufPool is a concurrency-safe pool of fixed-size byte slices.
// Callers must return slices via Put once they are done using them.
//
// Using a pool eliminates per-file heap allocations on the hot hashing
// path — a critical optimisation when hashing thousands of files where each
// 64 KiB allocation would otherwise pressure the GC.
type BufPool struct {
	pool sync.Pool
}

// NewBufPool creates a BufPool whose slices have the given capacity.
// A size of zero defaults to [defaultBufSize].
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

// Get returns a buffer from the pool. The caller must call Put to recycle it.
func (p *BufPool) Get() *[]byte { return p.pool.Get().(*[]byte) }

// Put returns buf to the pool. The buffer must not be used after this call.
func (p *BufPool) Put(buf *[]byte) { p.pool.Put(buf) }

// HashPool is a concurrency-safe pool of reusable hash.Hash instances.
//
// Each hash is Reset() before being returned from Get, making it safe to
// reuse across files without re-allocating the underlying hash state.
type HashPool struct {
	pool sync.Pool
}

// NewHashPool creates a HashPool that allocates hash instances via factory.
func NewHashPool(factory HashFactory) *HashPool {
	return &HashPool{
		pool: sync.Pool{
			New: func() any { return factory() },
		},
	}
}

// Get returns a reset hash.Hash from the pool.
func (p *HashPool) Get() hash.Hash {
	h := p.pool.Get().(hash.Hash)
	h.Reset()
	return h
}

// Put returns h to the pool. The hash must not be used after this call.
func (p *HashPool) Put(h hash.Hash) { p.pool.Put(h) }

// sharedBufPool is the package-level buffer pool used when callers do not
// supply their own.  Declared here so all internal callers share a single pool
// rather than each maintaining private ones.
var sharedBufPool = NewBufPool(defaultBufSize)
