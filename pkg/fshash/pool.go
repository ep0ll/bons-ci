package fshash

import (
	"runtime"
	"sync"
	"unsafe"
)

// Tiered buffer pools — SKILL §2.
//
// Buffer tiers are chosen to match OS read-ahead windows and disk bandwidth:
//   - smallBuf  (4 KiB):  tiny files; fits in a single read syscall.
//   - mediumBuf (64 KiB): source/config files; one or two reads.
//   - largeBuf  (1 MiB):  binaries and data; saturates sequential I/O.
//   - xlargeBuf (4 MiB):  large blobs; maximises read bandwidth.
//
// getBufForSize(0) MUST return small pool (SKILL §2 invariant).
// getBuf()          MUST return large pool (streaming where size is unknown).
const (
	smallBufSize  = 4 * 1024        // 4 KiB
	mediumBufSize = 64 * 1024       // 64 KiB
	largeBufSize  = 1 * 1024 * 1024 // 1 MiB
	xlargeBufSize = 4 * 1024 * 1024 // 4 MiB
)

var (
	smallPool  = newBufPool(smallBufSize)
	mediumPool = newBufPool(mediumBufSize)
	largePool  = newBufPool(largeBufSize)
	xlargePool = newBufPool(xlargeBufSize)
)

func newBufPool(size int) *sync.Pool {
	return &sync.Pool{New: func() any {
		b := make([]byte, size)
		return &b
	}}
}

// getBufForSize returns the SMALLEST pool whose buffer fits size bytes.
// size <= 0 returns the small pool (covers empty files and zero-size queries).
func getBufForSize(size int64) (*[]byte, int64) {
	switch {
	case size <= smallBufSize:
		return smallPool.Get().(*[]byte), smallBufSize
	case size <= mediumBufSize:
		return mediumPool.Get().(*[]byte), mediumBufSize
	case size <= largeBufSize:
		return largePool.Get().(*[]byte), largeBufSize
	default:
		return xlargePool.Get().(*[]byte), xlargeBufSize
	}
}

// getBuf returns a 1 MiB large buffer for streaming / unknown-size reads
// (used by HashReader). SKILL §2: "unknown size → large pool".
func getBuf() (*[]byte, int64) {
	return largePool.Get().(*[]byte), largeBufSize
}

// putBuf returns b to the correct pool, keyed by cap(*b).
// A buffer that does not match any known size goes to xlargePool (best-effort).
func putBuf(b *[]byte) {
	switch cap(*b) {
	case smallBufSize:
		smallPool.Put(b)
	case mediumBufSize:
		mediumPool.Put(b)
	case largeBufSize:
		largePool.Put(b)
	default:
		xlargePool.Put(b)
	}
}

// ── digest helpers ────────────────────────────────────────────────────────────

// maxDigestSize covers SHA-512 (64 B), Blake3 (32 B), xxHash3/64 (8 B),
// CRC32C (4 B). Stack-allocate once; never reallocate for any built-in algo.
const maxDigestSize = 64

// digestSink is a stack-allocated buffer for zero-alloc h.Sum output.
// SKILL §5: avoids one heap allocation per file digest.
type digestSink struct{ buf [maxDigestSize]byte }

func (s *digestSink) sum(h interface{ Sum(b []byte) []byte }) []byte {
	return h.Sum(s.buf[:0])
}

func cloneDigest(d []byte) []byte {
	cp := make([]byte, len(d))
	copy(cp, d)
	return cp
}

// ── workerPool ────────────────────────────────────────────────────────────────

// workerPool — SKILL §10: fixed goroutines, buffered channel, close-to-stop.
//
// The 56-byte pad between jobs and wg places them on separate cache lines,
// eliminating false sharing between the producer (writing to jobs) and the
// workers (decrementing the WaitGroup counter).
type workerPool struct {
	jobs chan func()
	_    [56]byte // cache-line pad: sizeof(chan) = 8; pad to 64-byte boundary
	wg   sync.WaitGroup
}

func newWorkerPool(n int) *workerPool {
	if n < 1 {
		n = runtime.NumCPU()
	}
	wp := &workerPool{jobs: make(chan func(), n*8)}
	for range n {
		wp.wg.Add(1)
		go func() {
			defer wp.wg.Done()
			for fn := range wp.jobs {
				fn()
			}
		}()
	}
	return wp
}

func (wp *workerPool) submit(fn func()) { wp.jobs <- fn }
func (wp *workerPool) stop()            { close(wp.jobs); wp.wg.Wait() }

// ── string/byte write helpers ─────────────────────────────────────────────────

// writeString writes s to h with zero allocation via unsafe.Slice.
// SKILL §5: avoids a []byte copy per name in hashDir.
func writeString(h interface{ Write([]byte) (int, error) }, s string) {
	if len(s) == 0 {
		return
	}
	b := unsafe.Slice(unsafe.StringData(s), len(s)) //nolint:gosec
	mustWriteBytes(h, b)
}

func mustWriteBytes(h interface{ Write([]byte) (int, error) }, p []byte) {
	if _, err := h.Write(p); err != nil {
		panic("fshash: hash.Write: " + err.Error())
	}
}
