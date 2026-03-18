package differ

import (
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

// TwoPhaseHasher is the default [ContentHasher]. It applies cheap metadata
// checks before falling back to full content hashing.
//
// # Phase 1 — metadata fast path (zero I/O)
//
//   - Size mismatch → definitively unequal.
//   - Same size + same mtime → assumed equal (atime/ctime ignored per spec).
//
// # Phase 2 — content slow path (I/O)
//
//   - Same size but different mtime → compute and compare hash digests.
//   - Buffers are drawn from BufPool; hash instances from HashPool.
//   - Zero allocations per file on the hot path once the pools are warm.
//
// # Symlinks
//
//   - Compared by link target string (os.Readlink), not target content.
//     When FollowSymlinks is true the classifier resolves symlinks before
//     hashing so this path is not exercised for symlinks.
//
// # Directories
//
//   - Compared by mode bits and mtime only; child equality is determined by
//     recursive classification, not by this hasher.
type TwoPhaseHasher struct {
	// BufPool is used for file-read buffers. Nil defaults to sharedBufPool.
	BufPool *BufPool
	// HashPool is used for hash.Hash instances. Nil defaults to a SHA-256 pool.
	HashPool *HashPool
}

// defaultTwoPhaseHasher is a package-level instance reusing the shared pools.
var defaultTwoPhaseHasher = &TwoPhaseHasher{}

// Equal implements [ContentHasher].
func (h *TwoPhaseHasher) Equal(lowerAbs, upperAbs string, lower, upper fs.FileInfo) (bool, error) {
	lMode := lower.Mode()
	if lMode.Type() != upper.Mode().Type() {
		return false, nil // type mismatch → always unequal
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
	// Phase 1: size is the cheapest discriminator — no I/O required.
	if lower.Size() != upper.Size() {
		return false, nil
	}
	// Phase 1: same size + same mtime → assume equal (BuildKit convention;
	// atime and ctime differences are intentionally ignored).
	if lower.ModTime().Equal(upper.ModTime()) {
		return true, nil
	}
	// Phase 2: content hashing.
	return h.hashEqual(lowerAbs, upperAbs)
}

// hashEqual computes and compares the hash digests of two files.
// Buffers and hash instances are drawn from the pools to avoid allocations.
func (h *TwoPhaseHasher) hashEqual(lowerAbs, upperAbs string) (bool, error) {
	lDigest, err := h.hashFile(lowerAbs)
	if err != nil {
		return false, fmt.Errorf("hash lower %q: %w", lowerAbs, err)
	}
	uDigest, err := h.hashFile(upperAbs)
	if err != nil {
		return false, fmt.Errorf("hash upper %q: %w", upperAbs, err)
	}
	return lDigest == uDigest, nil
}

// hashFile computes the hash digest of the regular file at path.
// It uses a pooled buffer (zero allocation on warm pool) and a pooled hash
// instance (Reset before use), ensuring the entire hot path is alloc-free.
func (h *TwoPhaseHasher) hashFile(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()

	bp := h.BufPool
	if bp == nil {
		bp = sharedBufPool
	}
	hp := h.HashPool
	if hp == nil {
		hp = sharedHashPool
	}

	buf := bp.Get()
	defer bp.Put(buf)

	hsh := hp.Get()
	defer hp.Put(hsh)

	// io.CopyBuffer uses the supplied buffer directly, avoiding internal
	// allocation and preventing the file contents from being copied through
	// an extra heap buffer. This is the closest to zero-copy we can achieve
	// in pure Go without mmap (see note below).
	//
	// NOTE — mmap alternative: for files significantly larger than defaultBufSize
	// (e.g. > 1 MiB), mapping the file with MAP_SHARED|PROT_READ and writing
	// the entire mapping to the hash avoids all read(2) syscall overhead.
	// A build-tag-gated helper can be added when profiling shows this is the
	// bottleneck:
	//
	//   data, _ := unix.Mmap(int(f.Fd()), 0, int(size),
	//       unix.PROT_READ, unix.MAP_SHARED|unix.MAP_POPULATE)
	//   defer unix.Munmap(data)
	//   hsh.Write(data)
	//
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
// values with hash equality information.
//
// # Pipeline semantics
//
// HashPipeline reads raw [CommonPath] values (HashEqual==nil) from an input
// channel, distributes them across a bounded worker pool, performs the two-phase
// content comparison, and writes enriched values (HashEqual set) to the output
// channel — preserving the streaming nature of the classifier output.
//
// # Concurrency
//
// Up to Workers goroutines may perform hashing concurrently. Because each
// worker uses independently pooled buffers and hash instances there is no lock
// contention on the hot path — hashing of N files proceeds in true parallel
// up to the I/O bandwidth limit of the underlying storage.
//
// # Back-pressure
//
// The output channel has the same buffer size as the input channel. When
// downstream consumers are slower than the hash workers, the workers block on
// the output send, which in turn blocks their semaphore slot, naturally
// limiting concurrency without requiring explicit rate-limiting logic.
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

// WithHashWorkers sets the maximum number of concurrent hash goroutines.
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
//
// It spawns a coordinator goroutine that drives up to Workers hash goroutines.
// The returned channel is closed when all input has been processed (or ctx
// is cancelled). Hashing errors are sent to errCh.
//
// lowerRoot and upperRoot are needed to resolve the absolute paths of each
// CommonPath entry for hashing. They are typically obtained from the
// [DirsyncClassifier] that produced the input channel.
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
		defer close(errCh) // signal downstream that no more errors will arrive

		// semaphore: at most workers concurrent hash goroutines.
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup

		for cp := range in {
			if ctx.Err() != nil {
				// Drain input without spawning new workers.
				continue
			}

			cp := cp // capture loop variable
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

// enrichOne performs the hash comparison for a single CommonPath and sets
// cp.HashEqual. Only regular files and symlinks are hashed; directories and
// special files are left with HashEqual==nil (no comparison performed).
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
		return // dirs and special files: no hash check
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
