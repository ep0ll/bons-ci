// Package pool provides power-of-2 sized byte-slice sync.Pools that eliminate
// heap allocations on the hot read/write path.
//
// Buckets cover [512 B, 4 MiB] in 14 power-of-2 steps. Sizes outside that
// range are allocated normally and are not pooled.
package pool

import (
	"math/bits"
	"sync"
)

const (
	minShift = 9  // 512 B
	maxShift = 22 // 4 MiB
	numPools = maxShift - minShift + 1

	// DefaultSize is the recommended intermediate buffer for streaming I/O (32 KiB).
	DefaultSize = 32 << 10
)

var pools [numPools]sync.Pool

func init() {
	for i := range pools {
		sz := 1 << (uint(i) + minShift)
		pools[i] = sync.Pool{New: func() any {
			b := make([]byte, sz)
			return &b
		}}
	}
}

// idx returns the pool bucket index for a request of at least size bytes.
func idx(size int) int {
	if size <= 0 {
		return 0
	}
	shift := bits.Len(uint(size - 1)) // ceil(log2(size))
	if shift < minShift {
		shift = minShift
	}
	if shift > maxShift {
		shift = maxShift
	}
	return shift - minShift
}

// Get returns a *[]byte with cap >= size from the pool.
// The slice's length is set equal to its capacity so the entire buffer is
// immediately usable without a manual reslice. Caller MUST call Put when done.
func Get(size int) *[]byte {
	b := pools[idx(size)].Get().(*[]byte)
	*b = (*b)[:cap(*b)]
	return b
}

// Put returns buf to the appropriate pool bucket.
// Passing a nil pointer, a nil slice, or a slice with capacity outside
// [512 B, 4 MiB] is a no-op and does not panic.
func Put(b *[]byte) {
	if b == nil || *b == nil {
		return
	}
	c := cap(*b)
	if c == 0 {
		return
	}
	shift := bits.Len(uint(c)) - 1 // floor(log2(c))
	if shift < minShift || shift > maxShift {
		return // outside pooled range; let GC collect
	}
	*b = (*b)[:c] // restore full length before returning
	pools[shift-minShift].Put(b)
}
