package differ

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// ContentHasher interface
// ─────────────────────────────────────────────────────────────────────────────

// ContentHasher compares two filesystem entries for content equality.
// Implementations must be safe for concurrent use from multiple goroutines.
//
// The interface is intentionally narrow — implementations may use metadata
// fast-paths, content hashing, or any other strategy without changing callers.
type ContentHasher interface {
	// Equal returns true when lower and upper are considered content-identical.
	//
	// lowerAbs and upperAbs are absolute paths. lower and upper are the
	// corresponding fs.FileInfo values (Lstat or Stat depending on
	// FollowSymlinks), provided so implementations may short-circuit on size
	// or mtime before performing any I/O.
	//
	// BuildKit DiffOp semantics: changes to atime and ctime alone must NOT be
	// treated as content changes. Only content bytes, permissions, and mtime
	// are considered.
	Equal(lowerAbs, upperAbs string, lower, upper fs.FileInfo) (bool, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// TwoPhaseHasher
// ─────────────────────────────────────────────────────────────────────────────

// defaultLargeFileThreshold is the file size above which the parallel
// segment comparison path is chosen over the sequential path.
//
// Below this threshold the overhead of spawning goroutines and opening file
// descriptors a second time outweighs the parallelism benefit. Empirically
// calibrated: for files under ~2 MiB on local SSD the sequential path is
// faster due to scheduler latency and OS buffer-cache warmup effects.
const defaultLargeFileThreshold = 2 << 20 // 2 MiB

// minSegmentSize is the minimum bytes assigned to each segment goroutine.
// Segments smaller than this don't justify the goroutine spawn and
// synchronization overhead.
const minSegmentSize = defaultBufSize * 4 // 4 × 64 KiB = 256 KiB

// TwoPhaseHasher is the default [ContentHasher].
//
// # Three-phase decision tree
//
//   Phase 1a — size (zero I/O, O(1)):
//     Different sizes → definitively not equal; no files opened.
//
//   Phase 1b — mtime (zero I/O, O(1)):
//     Same size + same mtime → assumed equal (BuildKit DiffOp convention;
//     atime and ctime differences are intentionally ignored).
//
//   Phase 2S — sequential incremental comparison (I/O, small files):
//     Used when file size < LargeFileThreshold (default 2 MiB).
//     Both files are read in lockstep one 64 KiB chunk at a time.
//     Returns false the moment any chunk differs — no further I/O.
//     Best case (differ in first chunk): 2 × 64 KiB of I/O.
//     Worst case (files equal):          2 × fileSize of I/O.
//
//   Phase 2P — parallel segment comparison (I/O, large files):
//     Used when file size ≥ LargeFileThreshold.
//     The file is divided into SegmentWorkers (default NumCPU) equal segments.
//     Each segment is compared by an independent goroutine using ReadAt
//     (pread64), which is concurrency-safe on the same file descriptor.
//     Any goroutine that finds a mismatch immediately cancels all others via
//     a shared context, bounding wasted I/O to O(one 64 KiB chunk × workers).
//
//     I/O cost for equal files:   2 × fileSize / NumCPU  (wall-clock time)
//     I/O cost for unequal files: as low as 2 × 64 KiB   (first chunk in any segment)
//
// # Why direct byte comparison rather than SHA-256
//
// SHA-256 is the right tool when you need a stored digest for later lookup
// or cache-key derivation. For a direct two-file equality check the hash is
// an unnecessary intermediary: if the bytes of A equal the bytes of B, a
// cryptographic digest of both would also match — but the byte comparison is
// cheaper because it avoids the SHA-256 compression function entirely. The
// hashFile method (used for standalone digest generation) is kept separately.
//
// # Symlinks
//
//   Compared by link-target string (os.Readlink), not target content.
//   When FollowSymlinks is true the classifier resolves symlinks upstream.
//
// # Directories
//
//   Compared by mode bits and mtime; child equality is determined by
//   recursive classification, not by this hasher.
type TwoPhaseHasher struct {
	// BufPool supplies file-read buffers. Nil → sharedBufPool (64 KiB).
	BufPool *BufPool

	// HashPool supplies hash.Hash instances for hashFile. Nil → sharedHashPool.
	HashPool *HashPool

	// LargeFileThreshold is the file size (bytes) at or above which the parallel
	// segment comparison path is used instead of the sequential path.
	// 0 defaults to defaultLargeFileThreshold (2 MiB).
	LargeFileThreshold int64

	// SegmentWorkers is the maximum number of goroutines that may compare
	// file segments concurrently during a large-file comparison.
	// 0 defaults to runtime.NumCPU().
	SegmentWorkers int
}

// defaultTwoPhaseHasher is a package-level instance reusing shared pools.
var defaultTwoPhaseHasher = &TwoPhaseHasher{}

// Equal implements [ContentHasher].
func (h *TwoPhaseHasher) Equal(lowerAbs, upperAbs string, lower, upper fs.FileInfo) (bool, error) {
	lMode := lower.Mode()
	if lMode.Type() != upper.Mode().Type() {
		return false, nil
	}

	switch {
	case lMode&fs.ModeSymlink != 0:
		return h.compareSymlinks(lowerAbs, upperAbs)
	case lMode.IsDir():
		return lMode.Perm() == upper.Mode().Perm() &&
			lower.ModTime().Equal(upper.ModTime()), nil
	case lMode.IsRegular():
		return h.compareRegular(lowerAbs, upperAbs, lower, upper)
	default:
		return lMode == upper.Mode(), nil
	}
}

func (h *TwoPhaseHasher) compareSymlinks(lowerAbs, upperAbs string) (bool, error) {
	lt, err := os.Readlink(lowerAbs)
	if err != nil {
		return false, err
	}
	ut, err := os.Readlink(upperAbs)
	if err != nil {
		return false, err
	}
	return lt == ut, nil
}

func (h *TwoPhaseHasher) compareRegular(
	lowerAbs, upperAbs string, lower, upper fs.FileInfo,
) (bool, error) {
	// Phase 1a — size (zero I/O).
	if lower.Size() != upper.Size() {
		return false, nil
	}
	// Phase 1b — mtime (zero I/O).
	// atime and ctime differences are intentionally ignored.
	if lower.ModTime().Equal(upper.ModTime()) {
		return true, nil
	}

	// Phase 2 — content comparison (I/O).
	// Route to the parallel path for large files.
	size := lower.Size()
	threshold := h.LargeFileThreshold
	if threshold <= 0 {
		threshold = defaultLargeFileThreshold
	}

	if size >= threshold {
		return h.compareContentsParallel(lowerAbs, upperAbs, size)
	}
	return h.compareContents(lowerAbs, upperAbs)
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 2S — sequential incremental comparison (small files)
// ─────────────────────────────────────────────────────────────────────────────

// compareContents compares two files by reading them sequentially in lockstep,
// chunk by chunk, returning false the instant any chunk differs.
//
// I/O is bounded by the position of the first mismatch:
//
//	differ at byte K  →  2 × ceil(K / 64KiB) × 64KiB  bytes of I/O
//	files are equal   →  2 × fileSize                  bytes of I/O
func (h *TwoPhaseHasher) compareContents(lowerAbs, upperAbs string) (bool, error) {
	lf, err := os.Open(lowerAbs)
	if err != nil {
		return false, fmt.Errorf("open lower %q: %w", lowerAbs, err)
	}
	defer lf.Close()

	uf, err := os.Open(upperAbs)
	if err != nil {
		return false, fmt.Errorf("open upper %q: %w", upperAbs, err)
	}
	defer uf.Close()

	bp := h.bufPool()

	// Two independent pooled buffers — one per file — so neither read blocks
	// on the other.
	lb := bp.Get()
	ub := bp.Get()
	defer bp.Put(lb)
	defer bp.Put(ub)

	lBuf, uBuf := *lb, *ub

	for {
		// ReadFull semantics:
		//   (n, nil)                 → full chunk; continue
		//   (n, io.ErrUnexpectedEOF) → partial final chunk; check then done
		//   (0, io.EOF)              → file exhausted
		ln, lErr := io.ReadFull(lf, lBuf)
		un, uErr := io.ReadFull(uf, uBuf)

		// Early-exit: count mismatch (O(1)) or content mismatch (O(chunk)).
		if ln != un || !bytes.Equal(lBuf[:ln], uBuf[:un]) {
			return false, nil
		}

		// Both hit EOF simultaneously with identical content → equal.
		if chunkAtEOF(lErr) && chunkAtEOF(uErr) {
			return true, nil
		}

		// One file exhausted before the other (modified between stat and read).
		if chunkAtEOF(lErr) != chunkAtEOF(uErr) {
			return false, nil
		}

		if lErr != nil {
			return false, fmt.Errorf("read lower %q: %w", lowerAbs, lErr)
		}
		if uErr != nil {
			return false, fmt.Errorf("read upper %q: %w", upperAbs, uErr)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 2P — parallel segment comparison (large files)
// ─────────────────────────────────────────────────────────────────────────────

// compareContentsParallel divides both files into SegmentWorkers equal segments
// and compares each segment concurrently using pread (ReadAt).
//
// # Why ReadAt (pread64) and not Read
//
// os.File.ReadAt uses the pread64 system call, which reads from an explicit
// byte offset without modifying the file's seek position. This makes it safe
// for multiple goroutines to issue concurrent reads on the same file descriptor
// — the kernel serialises the pread calls at the VFS layer while still
// allowing parallel execution in kernel SMP configurations and overlapping I/O
// queue entries on NVMe devices.
//
// Using Read (lseek + read) would require per-goroutine file descriptors or a
// mutex around every (lseek, read) pair, which would negate the parallelism.
//
// # Cancellation semantics
//
// An internal context is created for coordinating segment goroutines. When any
// goroutine finds a mismatch it calls cancel(), which signals all other segment
// goroutines to stop at their next chunk boundary. The maximum wasted I/O after
// a mismatch is O(one 64 KiB chunk × active workers) — one in-flight read per
// goroutine that hasn't yet checked the context.
//
// This internal context is independent of the caller's context. If the caller
// cancels (e.g. pipeline shutdown), the file reads will return errors naturally
// via the I/O path; those errors are propagated normally.
func (h *TwoPhaseHasher) compareContentsParallel(lowerAbs, upperAbs string, size int64) (bool, error) {
	lf, err := os.Open(lowerAbs)
	if err != nil {
		return false, fmt.Errorf("open lower %q: %w", lowerAbs, err)
	}
	defer lf.Close()

	uf, err := os.Open(upperAbs)
	if err != nil {
		return false, fmt.Errorf("open upper %q: %w", upperAbs, err)
	}
	defer uf.Close()

	workers := h.SegmentWorkers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// Compute the number of segments and the size of each.
	//
	// segSize = max(size / workers, minSegmentSize)
	// This ensures we never spawn goroutines for segments so small that the
	// spawn overhead exceeds the I/O savings.
	segSize := size / int64(workers)
	if segSize < minSegmentSize {
		segSize = minSegmentSize
	}
	numSegs := int((size + segSize - 1) / segSize)

	// Internal context: the first goroutine to find a mismatch cancels all
	// others, bounding wasted I/O to O(one 64 KiB chunk × active workers).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Each goroutine writes its result to a dedicated slot — no locks needed
	// on the results slice since indices are disjoint.
	type segResult struct {
		equal bool
		err   error
	}
	results := make([]segResult, numSegs)

	var wg sync.WaitGroup
	for i := range numSegs {
		offset := int64(i) * segSize
		length := segSize
		if i == numSegs-1 {
			// Last segment takes all remaining bytes.
			length = size - offset
		}

		wg.Add(1)
		go func(idx int, off, ln int64) {
			defer wg.Done()
			eq, err := h.compareSegment(ctx, lf, uf, off, ln, lowerAbs, upperAbs)
			results[idx] = segResult{equal: eq, err: err}
			if !eq || err != nil {
				// Mismatch (or error) found: cancel sibling goroutines so they
				// stop at their next chunk boundary without reading further.
				cancel()
			}
		}(i, offset, length)
	}
	wg.Wait()

	// Collect: any mismatch or error wins regardless of segment order.
	for _, r := range results {
		if r.err != nil {
			return false, r.err
		}
		if !r.equal {
			return false, nil
		}
	}
	return true, nil
}

// compareSegment compares the [offset, offset+length) byte range of lf and uf
// using ReadAt (pread64), in 64 KiB sub-chunks.
//
// It returns false at the first sub-chunk where the bytes differ, without
// reading the rest of the segment. It also honours the cancellation context:
// if another segment found a mismatch, the context is cancelled and this
// goroutine returns (false, nil) at the next chunk boundary.
//
// ReadAt contract:
//
//	(n == want, nil or io.EOF) → full chunk read; may continue
//	(n < want,  io.EOF)        → partial read at end of file; done
//	(n < want,  other error)   → real I/O error; propagate
func (h *TwoPhaseHasher) compareSegment(
	ctx context.Context,
	lf, uf *os.File,
	offset, length int64,
	lowerAbs, upperAbs string,
) (bool, error) {
	bp := h.bufPool()

	// Pooled buffers: one per file, no cross-buffer sharing.
	lb := bp.Get()
	ub := bp.Get()
	defer bp.Put(lb)
	defer bp.Put(ub)

	end := offset + length
	pos := offset

	for pos < end {
		// Honour cancellation from a sibling segment that already found a
		// mismatch. Checking here (once per chunk) bounds wasted I/O to
		// O(one chunk) after a cancel signal arrives.
		if ctx.Err() != nil {
			return false, nil
		}

		// Clamp the read to the segment boundary so segments don't overlap.
		want := int64(len(*lb))
		if end-pos < want {
			want = end - pos
		}
		lChunk := (*lb)[:want]
		uChunk := (*ub)[:want]

		// ReadAt (pread64): position-independent, safe for concurrent callers
		// on the same file descriptor.
		ln, lErr := lf.ReadAt(lChunk, pos)
		un, uErr := uf.ReadAt(uChunk, pos)

		// Early-exit: count or content mismatch.
		if ln != un || !bytes.Equal(lChunk[:ln], uChunk[:un]) {
			return false, nil
		}

		// When ReadAt returns fewer bytes than requested, the file ended within
		// this chunk. Since ln == un and the bytes matched, this segment is
		// equal up to EOF. Return without advancing pos further.
		if ln < int(want) {
			return true, nil
		}

		pos += int64(ln)

		// io.EOF with n == want is legal per ReadAt's contract ("may return
		// either err == EOF or err == nil" at end of file). It is not an error.
		if lErr == io.EOF || uErr == io.EOF {
			// We successfully read a full chunk that happened to be exactly at
			// the end of the file. The loop will exit naturally on the next
			// iteration when pos >= end (or ReadAt returns 0).
			continue
		}

		// Propagate real I/O errors.
		if lErr != nil {
			return false, fmt.Errorf("read lower %q at %d: %w", lowerAbs, pos, lErr)
		}
		if uErr != nil {
			return false, fmt.Errorf("read upper %q at %d: %w", upperAbs, pos, uErr)
		}
	}

	return true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────────────────────────────────────

// chunkAtEOF reports whether err signals end-of-file during a ReadFull call.
// Not used by the ReadAt path (which has different EOF semantics).
func chunkAtEOF(err error) bool {
	return err == io.EOF || err == io.ErrUnexpectedEOF
}

// bufPool returns the hasher's BufPool, falling back to the shared pool.
func (h *TwoPhaseHasher) bufPool() *BufPool {
	if h.BufPool != nil {
		return h.BufPool
	}
	return sharedBufPool
}

// hashPool returns the hasher's HashPool, falling back to the shared pool.
func (h *TwoPhaseHasher) hashPool() *HashPool {
	if h.HashPool != nil {
		return h.HashPool
	}
	return sharedHashPool
}

// hashFile computes the SHA-256 digest of the file at path.
//
// NOT used in the primary comparison path (compareContents / compareSegment
// perform direct byte equality checks without hashing). Provided for callers
// that need a standalone file identity: content-addressable caches, audit
// logs, or external tooling that stores digests for later comparison.
func (h *TwoPhaseHasher) hashFile(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()

	buf := h.bufPool().Get()
	defer h.bufPool().Put(buf)

	hsh := h.hashPool().Get()
	defer h.hashPool().Put(hsh)

	if _, err := io.CopyBuffer(hsh, f, *buf); err != nil {
		return [32]byte{}, err
	}

	var digest [32]byte
	hsh.Sum(digest[:0])
	return digest, nil
}

// sharedHashPool is the package-level HashPool using SHA-256.
var sharedHashPool = NewHashPool(DefaultHashFactory)

// ─────────────────────────────────────────────────────────────────────────────
// HashPipeline — parallel hash enrichment stage
// ─────────────────────────────────────────────────────────────────────────────

// HashPipeline is a concurrent transformation stage that enriches [CommonPath]
// values with content equality information.
//
// # Two levels of parallelism
//
// HashPipeline provides file-level parallelism: up to Workers goroutines
// compare different files simultaneously, each backed by a TwoPhaseHasher.
//
// For large files (≥ TwoPhaseHasher.LargeFileThreshold) the TwoPhaseHasher
// itself provides segment-level parallelism: a single file comparison spawns
// SegmentWorkers goroutines that read different byte ranges of the same file
// pair concurrently using pread64.
//
// The two levels compose independently:
//
//	N files × M workers (file-level) × P segment workers (per-file, large files only)
//
// # Back-pressure
//
// The output channel has the same buffer size as the input channel. Slow
// downstream consumers cause worker goroutines to block on the send, which
// fills the semaphore, which naturally limits concurrency without extra
// rate-limiting logic.
type HashPipeline struct {
	// Hasher is used to compare file pairs. Nil defaults to defaultTwoPhaseHasher.
	Hasher ContentHasher
	// Workers is the maximum number of concurrent hash goroutines.
	// Defaults to runtime.NumCPU().
	Workers int
	// BufPool and HashPool are forwarded to TwoPhaseHasher when Hasher is nil.
	BufPool  *BufPool
	HashPool *HashPool
}

// NewHashPipeline constructs a [HashPipeline] with sensible defaults.
func NewHashPipeline(opts ...HashPipelineOption) *HashPipeline {
	p := &HashPipeline{Workers: runtime.NumCPU()}
	for _, o := range opts {
		o(p)
	}
	if p.Hasher == nil {
		p.Hasher = &TwoPhaseHasher{BufPool: p.BufPool, HashPool: p.HashPool}
	}
	return p
}

// HashPipelineOption is a functional option for [HashPipeline].
type HashPipelineOption func(*HashPipeline)

// WithHasher replaces the default [TwoPhaseHasher] with a custom implementation.
func WithHasher(h ContentHasher) HashPipelineOption {
	return func(p *HashPipeline) { p.Hasher = h }
}

// WithHashWorkers sets the maximum number of concurrent file-comparison goroutines.
// This is the file-level concurrency; for intra-file segment parallelism on large
// files configure [TwoPhaseHasher.SegmentWorkers] via a custom hasher.
func WithHashWorkers(n int) HashPipelineOption {
	return func(p *HashPipeline) {
		if n > 0 {
			p.Workers = n
		}
	}
}

// WithBufPool sets the buffer pool forwarded to the default TwoPhaseHasher.
func WithBufPool(bp *BufPool) HashPipelineOption {
	return func(p *HashPipeline) { p.BufPool = bp }
}

// WithHashPool sets the hash pool forwarded to the default TwoPhaseHasher.
func WithHashPool(hp *HashPool) HashPipelineOption {
	return func(p *HashPipeline) { p.HashPool = hp }
}

// Run starts the hash enrichment pipeline and returns the enriched channel.
// Errors (including per-file comparison errors) are forwarded to errCh.
// errCh is closed when all input has been processed, signalling the downstream
// error-forwarder goroutine that it may terminate.
func (p *HashPipeline) Run(
	ctx context.Context,
	lowerRoot, upperRoot string,
	in <-chan CommonPath,
	errCh chan<- error,
) <-chan CommonPath {
	workers := p.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	out := make(chan CommonPath, cap(in)+workers)

	go func() {
		defer close(out)
		defer close(errCh)

		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup

		for cp := range in {
			if ctx.Err() != nil {
				continue // drain without spawning new workers
			}

			cp := cp
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				p.enrichOne(ctx, lowerRoot, upperRoot, &cp, errCh)

				select {
				case out <- cp:
				case <-ctx.Done():
				}
			}()
		}
		wg.Wait()
	}()

	return out
}

// enrichOne performs the content comparison for a single CommonPath and
// sets cp.HashEqual. Only regular files and symlinks are compared; directories
// and special files are left with HashEqual==nil.
func (p *HashPipeline) enrichOne(
	ctx context.Context,
	lowerRoot, upperRoot string,
	cp *CommonPath,
	errCh chan<- error,
) {
	if ctx.Err() != nil {
		return
	}
	switch cp.Kind {
	case PathKindFile, PathKindSymlink:
		// fall through
	default:
		return
	}

	hasher := p.Hasher
	if hasher == nil {
		hasher = defaultTwoPhaseHasher
	}

	lAbs := filepath.Join(lowerRoot, filepath.FromSlash(cp.Path))
	uAbs := filepath.Join(upperRoot, filepath.FromSlash(cp.Path))

	eq, err := hasher.Equal(lAbs, uAbs, cp.LowerInfo, cp.UpperInfo)
	if err != nil {
		sendErr(ctx, errCh, fmt.Errorf("hash %q: %w", cp.Path, err))
		return
	}
	v := eq
	cp.HashEqual = &v
}
