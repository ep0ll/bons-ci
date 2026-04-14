package registry

// bufpool provides power-of-2 sized byte-slice pools (512 B – 4 MiB) to
// eliminate heap allocations on the hot read/write I/O path.
// Functions are package-private; callers outside this package use pool/.

import (
	"math/bits"
	"sync"
)

const (
	bpMinShift = 9  // 512 B
	bpMaxShift = 22 // 4 MiB
	bpCount    = bpMaxShift - bpMinShift + 1
	bpDefault  = 32 << 10 // 32 KiB default streaming chunk
)

var bpools [bpCount]sync.Pool

func init() {
	for i := range bpools {
		sz := 1 << (uint(i) + bpMinShift)
		bpools[i] = sync.Pool{New: func() any {
			b := make([]byte, sz)
			return &b
		}}
	}
}

func bpIdx(size int) int {
	if size <= 0 {
		return 0
	}
	s := bits.Len(uint(size - 1))
	if s < bpMinShift {
		s = bpMinShift
	}
	if s > bpMaxShift {
		s = bpMaxShift
	}
	return s - bpMinShift
}

// poolGet returns a *[]byte with cap >= size. Caller MUST call poolPut when done.
func poolGet(size int) *[]byte {
	b := bpools[bpIdx(size)].Get().(*[]byte)
	*b = (*b)[:cap(*b)]
	return b
}

// poolPut returns b to the appropriate pool bucket.
func poolPut(b *[]byte) {
	if b == nil || *b == nil {
		return
	}
	c := cap(*b)
	if c == 0 {
		return
	}
	s := bits.Len(uint(c)) - 1
	if s < bpMinShift || s > bpMaxShift {
		return
	}
	*b = (*b)[:c]
	bpools[s-bpMinShift].Put(b)
}
