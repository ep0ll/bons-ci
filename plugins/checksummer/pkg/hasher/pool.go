// hasher/pool.go – size-classed buffer pool.
//
// Manages sync.Pool instances for power-of-two sizes from 4 KiB to 16 MiB.
// Every chunk-sized buffer in the parallel hasher comes from this pool,
// achieving zero GC pressure on the hot IO path.
package hasher

import (
	"math/bits"
	"sync"
)

const (
	minLog2    = 12 // 4 KiB
	maxLog2    = 24 // 16 MiB
	numClasses = maxLog2 - minLog2 + 1

	// MinBufSize is the smallest pooled buffer size.
	MinBufSize = 1 << minLog2 // 4 KiB
	// MaxBufSize is the largest pooled buffer size.
	MaxBufSize = 1 << maxLog2 // 16 MiB
)

// BufferPool is a set of sync.Pool instances, one per size class.
// Safe for concurrent use.
type BufferPool struct {
	pools [numClasses]sync.Pool
}

// NewBufferPool returns a ready-to-use BufferPool.
func NewBufferPool() *BufferPool {
	bp := &BufferPool{}
	for i := range bp.pools {
		classSize := 1 << (minLog2 + i)
		bp.pools[i].New = func() any {
			buf := make([]byte, classSize)
			return &buf
		}
	}
	return bp
}

// classIndex returns the pool index for size (rounds up to next power-of-two).
// Returns -1 for size > MaxBufSize (caller should allocate directly).
func classIndex(size int) int {
	if size <= 0 {
		return 0
	}
	if size > MaxBufSize {
		return -1
	}
	if size <= MinBufSize {
		return 0
	}
	log2 := bits.Len(uint(size - 1)) // ceiling log2
	idx := log2 - minLog2
	if idx < 0 {
		return 0
	}
	if idx >= numClasses {
		return numClasses - 1
	}
	return idx
}

// GetAtLeast returns a buffer of len >= size.
// Callers MUST call Put with the exact slice returned.
func (bp *BufferPool) GetAtLeast(size int) []byte {
	idx := classIndex(size)
	if idx < 0 {
		return make([]byte, size)
	}
	ptr := bp.pools[idx].Get().(*[]byte)
	return *ptr
}

// GetExact returns a slice of exactly size bytes backed by pool memory.
func (bp *BufferPool) GetExact(size int) []byte {
	buf := bp.GetAtLeast(size)
	if len(buf) < size {
		return buf
	}
	return buf[:size]
}

// Put returns buf to the appropriate pool.
// buf MUST have been obtained from GetAtLeast or GetExact.
func (bp *BufferPool) Put(buf []byte) {
	size := cap(buf)
	idx := classIndex(size)
	if idx < 0 {
		return // over-sized: let GC reclaim
	}
	full := buf[:size]
	bp.pools[idx].Put(&full)
}

// ClassSize returns the actual allocated capacity for a requested size.
func ClassSize(size int) int {
	idx := classIndex(size)
	if idx < 0 {
		return size
	}
	return 1 << (minLog2 + idx)
}

// ─────────────────────────── Package-level shared pool ───────────────────────

var sharedPool = NewBufferPool()

// GetBuf returns a buffer from the package-level shared pool.
func GetBuf(size int) []byte { return sharedPool.GetAtLeast(size) }

// PutBuf returns a buffer to the package-level shared pool.
func PutBuf(buf []byte) { sharedPool.Put(buf) }
