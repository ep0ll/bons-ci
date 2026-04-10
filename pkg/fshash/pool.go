package fshash

import (
	"hash"
	"runtime"
	"sync"
	"unsafe"
)

// ── Tiered buffer pools ───────────────────────────────────────────────────────
//
// Three pools cover the common file-size distribution:
//
//   small  (  4 KiB) — config files, source code, tiny assets
//   medium ( 64 KiB) — typical binary/data files          [default]
//   large  (  1 MiB) — media, archives, large blobs
//
// Callers obtain the smallest pool whose buffer fits the file.  Using an
// oversized buffer wastes memory and increases GC scan time; an undersized
// buffer forces multiple Read syscalls.

const (
	smallBufSize  = 4 * 1024    //   4 KiB
	mediumBufSize = 64 * 1024   //  64 KiB
	largeBufSize  = 1024 * 1024 //   1 MiB
)

var (
	smallPool  = newBufPool(smallBufSize)
	mediumPool = newBufPool(mediumBufSize)
	largePool  = newBufPool(largeBufSize)
)

func newBufPool(size int) *sync.Pool {
	return &sync.Pool{New: func() any {
		b := make([]byte, size)
		return &b
	}}
}

// getBufForSize returns a buffer appropriate for reading a file of the given
// size.  The caller MUST return it with putBuf using the same size.
func getBufForSize(size int64) (*[]byte, int64) {
	switch {
	case size > 0 && size <= smallBufSize:
		return smallPool.Get().(*[]byte), smallBufSize
	case size <= mediumBufSize:
		return mediumPool.Get().(*[]byte), mediumBufSize
	default:
		return largePool.Get().(*[]byte), largeBufSize
	}
}

// putBuf returns a buffer to its originating pool.
func putBuf(b *[]byte) {
	switch cap(*b) {
	case smallBufSize:
		smallPool.Put(b)
	case mediumBufSize:
		mediumPool.Put(b)
	default:
		largePool.Put(b)
	}
}

// ── Zero-alloc digest sink ────────────────────────────────────────────────────
//
// hash.Hash.Sum(b) appends the hash to b and returns the extended slice.
// Using Sum(nil) always allocates a new []byte.  Using Sum(buf[:0]) where buf
// is a stack-allocated array re-uses stack memory — the GC never sees it.
// We expose this pattern via digestSink which callers embed on the stack.

// maxDigestSize is the largest hash output we support (SHA-512 = 64 bytes).
const maxDigestSize = 64

// digestSink is a stack-allocated accumulator for hash.Sum output.
// Zero-value is ready to use.
type digestSink struct {
	buf [maxDigestSize]byte
}

// sum calls h.Sum into the sink's backing array and returns the live slice.
// The returned slice is valid until the next call to sum or until the sink
// goes out of scope — callers that need to store the digest must copy it.
func (s *digestSink) sum(h interface{ Sum(b []byte) []byte }) []byte {
	return h.Sum(s.buf[:0])
}

// clone returns a heap copy of the digest (safe to escape the stack frame).
func cloneDigest(d []byte) []byte {
	cp := make([]byte, len(d))
	copy(cp, d)
	return cp
}

// ── workerPool ────────────────────────────────────────────────────────────────
//
// Design:
//   - Fixed number of goroutines (= Workers option, capped at 64).
//   - Work items transmitted as func() over a buffered channel.
//   - Channel buffer = Workers*8 so producers rarely block.
//   - No context parameter — callers check ctx.Err() inside the func.
//   - stop() closes the channel; goroutines drain and exit cleanly.
//
// False-sharing avoidance: the WaitGroup counter is in a separate cache line
// from the channel pointer by padding the struct.

type workerPool struct {
	jobs chan func()
	// Pad to a cache line to avoid false sharing between the channel header
	// (pointer, 8 bytes) and the WaitGroup counter on adjacent cache lines.
	// chan header is 8 bytes on 64-bit; pad 56 bytes to reach 64-byte alignment.
	_  [56]byte
	wg sync.WaitGroup
}

func newWorkerPool(n int) *workerPool {
	if n < 1 {
		n = runtime.NumCPU()
	}
	wp := &workerPool{
		jobs: make(chan func(), n*8),
	}
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

// submit enqueues fn.  Blocks when the internal queue is full.
// Must not be called after stop().
func (wp *workerPool) submit(fn func()) { wp.jobs <- fn }

// stop drains the queue and waits for all goroutines to exit.
// Must be called exactly once.
func (wp *workerPool) stop() {
	close(wp.jobs)
	wp.wg.Wait()
}

// getBuf returns a medium-sized (64 KiB) buffer from the shared pool.
// Use this when the file size is unknown (e.g. streaming readers).
// The caller must return the buffer with putBuf.
func getBuf() *[]byte { return mediumPool.Get().(*[]byte) }

// ── String → hash write without []byte alloc ─────────────────────────────────
//
// hash.Hash embeds io.Writer.  io.WriteString checks whether the writer
// implements io.StringWriter and avoids the []byte conversion if it does.
// Standard library hash implementations (sha256 etc.) do NOT implement
// io.StringWriter, so we use unsafe.Slice to produce a []byte header over
// the string's backing storage without copying.

// writeString writes s to h without allocating a []byte intermediate.
// h must be a hash.Hash; the broader interface is kept for zero-cast convenience.
func writeString(h hash.Hash, s string) {
	if len(s) == 0 {
		return
	}
	//nolint:gosec // safe: slice shares string's memory for the Write call duration
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	mustWrite(h, b)
}
