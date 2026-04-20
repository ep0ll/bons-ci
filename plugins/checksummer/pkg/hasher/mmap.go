//go:build linux

// hasher/mmap.go – mmap-based hasher + adaptive strategy selector.
package hasher

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

// ─────────────────────────── MmapHasher ──────────────────────────────────────

// MmapHasher hashes files via MAP_SHARED|MAP_POPULATE + MADV_SEQUENTIAL.
// Zero userspace copy: kernel feeds pages directly to the hash function.
// Safe for concurrent use.
type MmapHasher struct {
	pool *BufferPool
}

func NewMmapHasher() *MmapHasher {
	return &MmapHasher{pool: NewBufferPool()}
}

func (m *MmapHasher) HashFile(ctx context.Context, path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("mmap: open %q: %w", path, err)
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("mmap: stat %q: %w", path, err)
	}
	return m.HashFD(ctx, fd, st.Size)
}

func (m *MmapHasher) HashFD(ctx context.Context, fd int, size int64) ([]byte, error) {
	if size == 0 {
		return EmptyHash(), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// MAP_POPULATE faults all pages in before hashing (sequential prefetch).
	data, err := unix.Mmap(fd, 0, int(size),
		unix.PROT_READ, unix.MAP_SHARED|unix.MAP_POPULATE)
	if err != nil {
		// Fallback: some overlayfs/FUSE setups reject mmap.
		seq := &Blake3Hasher{pool: m.pool, bufSize: defaultReadBufSize}
		return seq.HashFD(ctx, fd, size)
	}
	defer func() { _ = unix.Munmap(data) }()

	_ = unix.Madvise(data, unix.MADV_SEQUENTIAL|unix.MADV_WILLNEED)
	defer func() { _ = unix.Madvise(data, unix.MADV_DONTNEED) }()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	dig := newDigest()
	if _, err := dig.Write(data); err != nil {
		return nil, fmt.Errorf("mmap: digest write: %w", err)
	}
	return dig.Sum(nil), nil
}

func (m *MmapHasher) HashReader(ctx context.Context, r io.Reader) ([]byte, error) {
	return (&Blake3Hasher{pool: m.pool, bufSize: defaultReadBufSize}).HashReader(ctx, r)
}

// ─────────────────────────── AdaptiveHasher ───────────────────────────────────

const (
	SmallFileThreshold  int64 = 8 << 20  // 8 MiB  → sequential
	MediumFileThreshold int64 = 128 << 20 // 128 MiB → mmap; above → parallel
)

// AdaptiveHasher selects the optimal strategy by file size.
type AdaptiveHasher struct {
	seq      *Blake3Hasher
	mmap     *MmapHasher
	parallel *ParallelHasher
	small    int64
	medium   int64
}

type AdaptiveOption func(*AdaptiveHasher)

func WithSmallThreshold(n int64) AdaptiveOption {
	return func(a *AdaptiveHasher) { a.small = n }
}
func WithMediumThreshold(n int64) AdaptiveOption {
	return func(a *AdaptiveHasher) { a.medium = n }
}
func WithParallelHasher(ph *ParallelHasher) AdaptiveOption {
	return func(a *AdaptiveHasher) { a.parallel = ph }
}

func NewAdaptiveHasher(opts ...AdaptiveOption) *AdaptiveHasher {
	a := &AdaptiveHasher{
		seq:      NewBlake3Hasher(0),
		mmap:     NewMmapHasher(),
		parallel: NewParallelHasher(),
		small:    SmallFileThreshold,
		medium:   MediumFileThreshold,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *AdaptiveHasher) HashFile(ctx context.Context, path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("adaptive: open %q: %w", path, err)
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("adaptive: stat %q: %w", path, err)
	}
	return a.HashFD(ctx, fd, st.Size)
}

func (a *AdaptiveHasher) HashFD(ctx context.Context, fd int, size int64) ([]byte, error) {
	switch {
	case size <= 0:
		return EmptyHash(), nil
	case size < a.small:
		return a.seq.HashFD(ctx, fd, size)
	case size < a.medium:
		return a.mmap.HashFD(ctx, fd, size)
	default:
		return a.parallel.HashFD(ctx, fd, size)
	}
}

func (a *AdaptiveHasher) HashReader(ctx context.Context, r io.Reader) ([]byte, error) {
	return a.seq.HashReader(ctx, r)
}

func (a *AdaptiveHasher) Strategy(size int64) Strategy {
	switch {
	case size < a.small:
		return StrategySequential
	case size < a.medium:
		return StrategyMmap
	default:
		return StrategyParallel
	}
}
