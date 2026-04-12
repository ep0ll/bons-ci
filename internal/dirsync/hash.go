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
type ContentHasher interface {
	// Equal returns true when lower and upper are considered content-identical.
	//
	// lowerAbs and upperAbs are absolute paths. lower and upper are the
	// corresponding fs.FileInfo values (Lstat or Stat depending on
	// FollowSymlinks), provided so implementations may short-circuit on size
	// or mtime before performing any I/O.
	//
	// BuildKit DiffOp semantics: changes to atime and ctime alone must NOT be
	// treated as content changes.
	Equal(lowerAbs, upperAbs string, lower, upper fs.FileInfo) (bool, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// TwoPhaseHasher
// ─────────────────────────────────────────────────────────────────────────────

// defaultLargeFileThreshold is the file size above which the parallel segment
// comparison path is chosen over the sequential path.
const defaultLargeFileThreshold = 2 << 20 // 2 MiB

// minSegmentSize is the minimum bytes assigned to each segment goroutine.
const minSegmentSize = defaultBufSize * 4 // 4 × 64 KiB = 256 KiB

// TwoPhaseHasher is the default [ContentHasher].
//
// # Three-phase decision tree
//
//	Phase 1a — size (zero I/O, O(1)):
//	  Different sizes → definitively not equal.
//
//	Phase 1b — mtime (zero I/O, O(1)):
//	  Same size + same mtime → assumed equal (BuildKit DiffOp convention;
//	  atime and ctime differences are intentionally ignored).
//
//	Phase 2S — sequential incremental comparison (small files):
//	  Used when file size < LargeFileThreshold (default 2 MiB).
//	  Reads both files in lockstep in 64 KiB chunks; returns false immediately
//	  on the first differing chunk.
//
//	Phase 2P — parallel segment comparison (large files):
//	  Used when file size ≥ LargeFileThreshold.
//	  Divides the file into SegmentWorkers equal segments compared concurrently
//	  using ReadAt (pread64). Any segment that finds a mismatch cancels the
//	  others via a shared context.
//
// # Why direct byte comparison rather than SHA-256
//
// For a direct two-file equality check, the hash is an unnecessary
// intermediary. Direct byte comparison is cheaper because it avoids the SHA-256
// compression function entirely. hashFile is kept for standalone digest use.
//
// # Symlinks
//
//	Compared by link-target string (os.Readlink), not target content.
//
// # Directories
//
//	Compared by mode bits and mtime.
type TwoPhaseHasher struct {
	// BufPool supplies file-read buffers. Nil → sharedBufPool (64 KiB).
	BufPool *BufPool

	// HashPool supplies hash.Hash instances for hashFile. Nil → sharedHashPool.
	HashPool *HashPool

	// LargeFileThreshold is the file size at or above which the parallel path
	// is used. 0 defaults to defaultLargeFileThreshold (2 MiB).
	LargeFileThreshold int64

	// SegmentWorkers is the maximum number of goroutines for large-file
	// segment comparison. 0 defaults to runtime.NumCPU().
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
	// Phase 1b — mtime (zero I/O). atime/ctime differences are ignored.
	if lower.ModTime().Equal(upper.ModTime()) {
		return true, nil
	}

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

// compareContents compares two files sequentially in lockstep, chunk by chunk.
// Returns false the instant any chunk differs.
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
	lb := bp.Get()
	ub := bp.Get()
	defer bp.Put(lb)
	defer bp.Put(ub)

	lBuf, uBuf := *lb, *ub

	for {
		ln, lErr := io.ReadFull(lf, lBuf)
		un, uErr := io.ReadFull(uf, uBuf)

		if ln != un || !bytes.Equal(lBuf[:ln], uBuf[:un]) {
			return false, nil
		}

		if chunkAtEOF(lErr) && chunkAtEOF(uErr) {
			return true, nil
		}
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
// and compares each segment concurrently using ReadAt (pread64).
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

	segSize := size / int64(workers)
	if segSize < minSegmentSize {
		segSize = minSegmentSize
	}
	numSegs := int((size + segSize - 1) / segSize)

	// Internal context: first goroutine to find a mismatch cancels the others.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
			length = size - offset
		}

		wg.Add(1)
		go func(idx int, off, ln int64) {
			defer wg.Done()
			eq, err := h.compareSegment(ctx, lf, uf, off, ln, lowerAbs, upperAbs)
			results[idx] = segResult{equal: eq, err: err}
			if !eq || err != nil {
				cancel()
			}
		}(i, offset, length)
	}
	wg.Wait()

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
// using ReadAt (pread64) in 64 KiB sub-chunks.
//
// BUG FIX M3: The original code checked `if lErr == io.EOF || uErr == io.EOF`
// and continued before checking `if lErr != nil`. This meant that if uErr was
// io.EOF (a legal end-of-chunk sentinel) and lErr was a real I/O error on the
// same iteration, lErr was silently discarded. Files with lower-side I/O errors
// could incorrectly appear equal.
//
// Fix: propagate real errors first, treat io.EOF as non-error only after.
func (h *TwoPhaseHasher) compareSegment(
	ctx context.Context,
	lf, uf *os.File,
	offset, length int64,
	lowerAbs, upperAbs string,
) (bool, error) {
	bp := h.bufPool()
	lb := bp.Get()
	ub := bp.Get()
	defer bp.Put(lb)
	defer bp.Put(ub)

	end := offset + length
	pos := offset

	for pos < end {
		if ctx.Err() != nil {
			return false, nil
		}

		want := int64(len(*lb))
		if end-pos < want {
			want = end - pos
		}
		lChunk := (*lb)[:want]
		uChunk := (*ub)[:want]

		ln, lErr := lf.ReadAt(lChunk, pos)
		un, uErr := uf.ReadAt(uChunk, pos)

		// Early-exit: count or content mismatch.
		if ln != un || !bytes.Equal(lChunk[:ln], uChunk[:un]) {
			return false, nil
		}

		// Partial read: file ended within this chunk. Since ln==un and bytes
		// matched, this segment is equal up to EOF.
		if ln < int(want) {
			return true, nil
		}

		pos += int64(ln)

		// BUG FIX M3: check real I/O errors before treating io.EOF as benign.
		// The original code did `if lErr == io.EOF || uErr == io.EOF { continue }`
		// BEFORE checking `if lErr != nil`, which caused real errors to be lost
		// when the other file returned io.EOF on the same iteration.
		if lErr != nil && lErr != io.EOF {
			return false, fmt.Errorf("read lower %q at %d: %w", lowerAbs, pos, lErr)
		}
		if uErr != nil && uErr != io.EOF {
			return false, fmt.Errorf("read upper %q at %d: %w", upperAbs, pos, uErr)
		}
		// io.EOF with n == want is legal per ReadAt's contract. The loop exits
		// naturally when pos >= end on the next iteration.
	}

	return true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────────────────────────────────────

// chunkAtEOF reports whether err signals end-of-file during a ReadFull call.
func chunkAtEOF(err error) bool {
	return err == io.EOF || err == io.ErrUnexpectedEOF
}

func (h *TwoPhaseHasher) bufPool() *BufPool {
	if h.BufPool != nil {
		return h.BufPool
	}
	return sharedBufPool
}

func (h *TwoPhaseHasher) hashPool() *HashPool {
	if h.HashPool != nil {
		return h.HashPool
	}
	return sharedHashPool
}

// hashFile computes the SHA-256 digest of the file at path.
// Not used in the primary comparison path; provided for callers that need a
// standalone file digest (content-addressable caches, audit logs, etc.).
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

var sharedHashPool = NewHashPool(DefaultHashFactory)

// ─────────────────────────────────────────────────────────────────────────────
// HashPipeline — parallel hash enrichment stage
// ─────────────────────────────────────────────────────────────────────────────

// HashPipeline is a concurrent transformation stage that enriches [CommonPath]
// values with content equality information.
//
// # Two levels of parallelism
//
// File-level: up to Workers goroutines compare different files simultaneously.
// Segment-level (large files only): TwoPhaseHasher spawns SegmentWorkers
// goroutines that read different byte ranges of the same file pair concurrently
// using pread64.
type HashPipeline struct {
	// Hasher is used to compare file pairs. Nil defaults to defaultTwoPhaseHasher.
	Hasher ContentHasher
	// Workers is the maximum number of concurrent hash goroutines.
	// Defaults to runtime.NumCPU().
	Workers  int
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
// Errors are forwarded to errCh. errCh is closed when all input is processed.
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
				continue // drain without spawning
			}

			cp := cp

			// Non-blocking select prevents deadlock if ctx is cancelled while
			// all worker slots are occupied.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				continue
			}

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

// enrichOne performs the content comparison for a single CommonPath and sets
// cp.HashEqual. Only regular files and symlinks are compared; directories and
// special files are left with HashEqual==nil.
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
