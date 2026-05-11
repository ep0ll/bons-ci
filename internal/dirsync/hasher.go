package dirsync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"sync"
	"syscall"

	"github.com/zeebo/xxh3"
)

// ─────────────────────────────────────────────────────────────────────────────
// ContentHasher interface
// ─────────────────────────────────────────────────────────────────────────────

// ContentHasher compares two filesystem entries for content equality.
// All implementations must be safe for concurrent use from multiple goroutines.
type ContentHasher interface {
	// Equal returns true when lower and upper are content-identical.
	//
	// lowerAbs, upperAbs: absolute file paths.
	// lowerInfo, upperInfo: FileInfo from Lstat (or Stat with FollowSymlinks).
	// Implementations use FileInfo for cheap short-circuits before doing I/O.
	//
	// BuildKit semantics: atime and ctime changes alone are NOT content changes.
	Equal(lowerAbs, upperAbs string, lowerInfo, upperInfo fs.FileInfo) (equal bool, err error)
}

// ─────────────────────────────────────────────────────────────────────────────
// Comparison constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	// defaultLargeFileThreshold is the file size in bytes at or above which
	// the parallel segment-comparison path (Tier 4P) is used.
	// 2 MiB is the empirical crossover where goroutine+pread64 overhead is
	// outweighed by parallelism gains on typical multi-core hardware.
	defaultLargeFileThreshold int64 = 2 << 20 // 2 MiB

	// minSegmentSize prevents allocating a segment so small that goroutine
	// overhead dominates I/O time. 256 KiB = 4 × 64 KiB read buffer.
	minSegmentSize int64 = defaultBufSize * 4 // 256 KiB
)

// ─────────────────────────────────────────────────────────────────────────────
// TwoPhaseHasher — four-tier comparison decision tree
// ─────────────────────────────────────────────────────────────────────────────

// TwoPhaseHasher is the default [ContentHasher].
//
// # Four-tier decision tree (cheapest → most expensive)
//
//	Tier 1 — Inode identity (zero I/O, O(1)):
//	  Same (dev, ino) → hard-linked: content definitionally identical.
//	  Only available on Unix where FileInfo.Sys() returns *syscall.Stat_t.
//
//	Tier 2 — Size mismatch (zero I/O, O(1)):
//	  Different sizes → definitively not equal. Zero file opens.
//
//	Tier 3 — Mtime equality (zero I/O, O(1)):
//	  Same size + same mtime → assumed equal. Ignores atime/ctime per
//	  BuildKit DiffOp convention to avoid false positives from access time
//	  updates and metadata-only changes.
//
//	Tier 4S — Sequential incremental comparison (files < LargeFileThreshold):
//	  Reads both files in lockstep in 64 KiB chunks.
//	  Returns false on the FIRST differing chunk (true early exit).
//	  Zero extra goroutines; best for small files where goroutine overhead
//	  would outweigh any parallelism benefit.
//
//	Tier 4P — Parallel segment comparison (files ≥ LargeFileThreshold):
//	  Divides file into SegmentWorkers equal byte ranges compared concurrently
//	  via pread64 (ReadAt). Any worker detecting a mismatch immediately
//	  cancels all others via context, providing early exit proportional to
//	  where the difference occurs — critical for large files with early diffs.
//
// # Content fingerprinting (xxh3)
//
// [fileDigest] uses xxh3 128-bit from [github.com/zeebo/xxh3], achieving
// ~25 GB/s on AVX2 hardware vs ~1 GB/s for SHA-256. Used for audit logs and
// content-addressable storage, NOT for the primary equality check.
type TwoPhaseHasher struct {
	// BufPool supplies read buffers. Nil uses the package-level shared pool.
	BufPool *BufPool

	// LargeFileThreshold: bytes at or above which Tier 4P is used.
	// Zero uses defaultLargeFileThreshold (2 MiB).
	LargeFileThreshold int64

	// SegmentWorkers: maximum goroutines for Tier 4P.
	// Zero uses runtime.NumCPU().
	SegmentWorkers int
}

// defaultTwoPhaseHasher is the package-level instance reusing the shared pool.
var defaultTwoPhaseHasher ContentHasher = &TwoPhaseHasher{}

// Equal implements [ContentHasher].
func (h *TwoPhaseHasher) Equal(
	lowerAbs, upperAbs string,
	lowerInfo, upperInfo fs.FileInfo,
) (bool, error) {
	if lowerInfo.Mode().Type() != upperInfo.Mode().Type() {
		return false, nil // type mismatch → not equal
	}
	switch {
	case lowerInfo.Mode()&fs.ModeSymlink != 0:
		return h.compareSymlinks(lowerAbs, upperAbs)
	case lowerInfo.IsDir():
		return h.compareDirectories(lowerInfo, upperInfo), nil
	case lowerInfo.Mode().IsRegular():
		return h.compareRegularFiles(lowerAbs, upperAbs, lowerInfo, upperInfo)
	default:
		return lowerInfo.Mode() == upperInfo.Mode(), nil
	}
}

// compareSymlinks compares two symlinks by their link-target string.
// Target content is NOT compared — only the target path matters.
func (h *TwoPhaseHasher) compareSymlinks(lowerAbs, upperAbs string) (bool, error) {
	lt, err := os.Readlink(lowerAbs)
	if err != nil {
		return false, fmt.Errorf("readlink lower %q: %w", lowerAbs, err)
	}
	ut, err := os.Readlink(upperAbs)
	if err != nil {
		return false, fmt.Errorf("readlink upper %q: %w", upperAbs, err)
	}
	return lt == ut, nil
}

// compareDirectories compares two directories by mode bits and mtime.
// Directory CONTENT is compared implicitly by the walker's recursive descent.
func (h *TwoPhaseHasher) compareDirectories(lowerInfo, upperInfo fs.FileInfo) bool {
	return lowerInfo.Mode().Perm() == upperInfo.Mode().Perm() &&
		lowerInfo.ModTime().Equal(upperInfo.ModTime())
}

// compareRegularFiles applies the four-tier decision tree to two regular files.
func (h *TwoPhaseHasher) compareRegularFiles(
	lowerAbs, upperAbs string,
	lowerInfo, upperInfo fs.FileInfo,
) (bool, error) {
	// Tier 1: Inode identity — cheapest possible check, zero I/O.
	if sameInode(lowerInfo, upperInfo) {
		return true, nil
	}
	// Tier 2: Size mismatch — zero I/O.
	if lowerInfo.Size() != upperInfo.Size() {
		return false, nil
	}
	// Tier 3: Mtime equality — zero I/O, BuildKit DiffOp semantics.
	if lowerInfo.ModTime().Equal(upperInfo.ModTime()) {
		return true, nil
	}
	// Tier 4: Content comparison — I/O required.
	if lowerInfo.Size() >= h.largeFileThreshold() {
		return h.compareContentsParallel(lowerAbs, upperAbs, lowerInfo.Size())
	}
	return h.compareContentsSequential(lowerAbs, upperAbs)
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 4S — sequential incremental comparison
// ─────────────────────────────────────────────────────────────────────────────

// compareContentsSequential reads both files in lockstep in 64 KiB chunks and
// returns false on the first differing chunk. No extra goroutines are started.
func (h *TwoPhaseHasher) compareContentsSequential(lowerAbs, upperAbs string) (bool, error) {
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

	pool := h.bufPool()
	lbPtr, ubPtr := pool.Get(), pool.Get()
	defer pool.Put(lbPtr)
	defer pool.Put(ubPtr)
	lBuf, uBuf := *lbPtr, *ubPtr

	for {
		ln, lErr := io.ReadFull(lf, lBuf)
		un, uErr := io.ReadFull(uf, uBuf)

		if ln != un || !bytes.Equal(lBuf[:ln], uBuf[:un]) {
			return false, nil // mismatch
		}
		if isEOFChunk(lErr) && isEOFChunk(uErr) {
			return true, nil // both files exhausted at same boundary
		}
		if isEOFChunk(lErr) != isEOFChunk(uErr) {
			return false, nil // one file ended before the other (defensive)
		}
		if lErr != nil {
			return false, fmt.Errorf("read lower %q: %w", lowerAbs, lErr)
		}
		if uErr != nil {
			return false, fmt.Errorf("read upper %q: %w", upperAbs, uErr)
		}
	}
}

// isEOFChunk reports whether err signals end-of-file in a ReadFull call.
func isEOFChunk(err error) bool {
	return err == io.EOF || err == io.ErrUnexpectedEOF
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 4P — parallel segment comparison
// ─────────────────────────────────────────────────────────────────────────────

// compareContentsParallel divides both files into SegmentWorkers equal ranges
// and compares each range concurrently via pread64 (ReadAt).
//
// The first goroutine to detect a mismatch cancels all others through the
// internal context, providing early exit proportional to where the difference
// occurs — critical for large files that differ near the beginning.
func (h *TwoPhaseHasher) compareContentsParallel(
	lowerAbs, upperAbs string, size int64,
) (bool, error) {
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

	workers := h.segmentWorkers()
	segSize := size / int64(workers)
	if segSize < minSegmentSize {
		segSize = minSegmentSize
	}
	numSegments := int((size + segSize - 1) / segSize)

	// Internal context: first mismatch cancels all other goroutines.
	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()

	type result struct {
		equal bool
		err   error
	}
	results := make([]result, numSegments)

	var wg sync.WaitGroup
	for i := 0; i < numSegments; i++ {
		i := i
		off := int64(i) * segSize
		ln := segSize
		if i == numSegments-1 {
			ln = size - off // last segment takes the remainder
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			eq, err := h.compareSegment(ctx, lf, uf, off, ln, lowerAbs, upperAbs)
			results[i] = result{equal: eq, err: err}
			if !eq || err != nil {
				cancelAll()
			}
		}()
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
// # Error ordering discipline
//
// Real I/O errors are checked BEFORE treating io.EOF as benign. If we treated
// io.EOF first (as in the original code), a real error on one file while the
// other returns io.EOF on the same iteration would be silently discarded,
// causing a misbehaving file to appear equal when it is not.
func (h *TwoPhaseHasher) compareSegment(
	ctx context.Context,
	lf, uf *os.File,
	offset, length int64,
	lowerAbs, upperAbs string,
) (bool, error) {
	pool := h.bufPool()
	lbPtr, ubPtr := pool.Get(), pool.Get()
	defer pool.Put(lbPtr)
	defer pool.Put(ubPtr)

	end, pos := offset+length, offset

	for pos < end {
		if ctx.Err() != nil {
			return false, nil // another segment detected a mismatch; stop.
		}
		chunkLen := int64(len(*lbPtr))
		if remaining := end - pos; remaining < chunkLen {
			chunkLen = remaining
		}
		lChunk, uChunk := (*lbPtr)[:chunkLen], (*ubPtr)[:chunkLen]

		ln, lErr := lf.ReadAt(lChunk, pos)
		un, uErr := uf.ReadAt(uChunk, pos)

		if ln != un || !bytes.Equal(lChunk[:ln], uChunk[:un]) {
			return false, nil
		}
		if ln < int(chunkLen) {
			return true, nil // EOF within chunk; bytes matched up to here
		}
		pos += int64(ln)

		// Check real errors BEFORE treating io.EOF as benign.
		if lErr != nil && lErr != io.EOF {
			return false, fmt.Errorf("read lower %q at %d: %w", lowerAbs, pos, lErr)
		}
		if uErr != nil && uErr != io.EOF {
			return false, fmt.Errorf("read upper %q at %d: %w", upperAbs, pos, uErr)
		}
	}
	return true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// fileDigest — xxh3 128-bit content fingerprint
// ─────────────────────────────────────────────────────────────────────────────

// fileDigest computes the xxh3 128-bit digest of the file at path.
//
// xxh3 from github.com/zeebo/xxh3 achieves ~25 GB/s on AVX2 hardware —
// ~25× faster than SHA-256 — making it ideal for content fingerprinting
// where cryptographic collision resistance is not required.
//
// NOT used in the primary equality hot path; provided for audit logs and
// content-addressable storage integrations that need a compact fingerprint.
func (h *TwoPhaseHasher) fileDigest(path string) (xxh3.Uint128, error) {
	f, err := os.Open(path)
	if err != nil {
		return xxh3.Uint128{}, fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	bufPtr := h.bufPool().Get()
	defer h.bufPool().Put(bufPtr)

	hsh := xxh3.New()
	if _, err := io.CopyBuffer(hsh, f, *bufPtr); err != nil {
		return xxh3.Uint128{}, fmt.Errorf("digest %q: %w", path, err)
	}
	return hsh.Sum128(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Inode identity check (Tier 1)
// ─────────────────────────────────────────────────────────────────────────────

// sameInode reports whether two FileInfo values refer to the same underlying
// inode. When true, the paths are hard links to the same file and content is
// definitionally identical — the cheapest possible equality check.
//
// Only meaningful on Unix where FileInfo.Sys() returns *syscall.Stat_t.
// Returns false on platforms where this information is unavailable.
func sameInode(a, b fs.FileInfo) bool {
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	return sa.Dev == sb.Dev && sa.Ino == sb.Ino
}

// ─────────────────────────────────────────────────────────────────────────────
// Configuration accessors
// ─────────────────────────────────────────────────────────────────────────────

func (h *TwoPhaseHasher) bufPool() *BufPool {
	if h.BufPool != nil {
		return h.BufPool
	}
	return sharedBufPool
}

func (h *TwoPhaseHasher) largeFileThreshold() int64 {
	if h.LargeFileThreshold > 0 {
		return h.LargeFileThreshold
	}
	return defaultLargeFileThreshold
}

func (h *TwoPhaseHasher) segmentWorkers() int {
	if h.SegmentWorkers > 0 {
		return h.SegmentWorkers
	}
	return runtime.NumCPU()
}
