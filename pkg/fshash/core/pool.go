package core

import (
	"sync"
	"unsafe"
)

// ── Tiered Buffer Pool ────────────────────────────────────────────────────────
//
// Four tiers tuned to storage bandwidth (SKILL §2):
//
//   Size range         Buffer   Why
//   ─────────────────  ───────  ──────────────────────────────────────────────
//   0 – 4 KiB          4 KiB   tiny files; one syscall
//   4 KiB – 64 KiB    64 KiB   source/config; 1–2 reads
//   64 KiB – 1 MiB     1 MiB   binaries; saturates sequential NVMe
//   > 1 MiB             4 MiB   large blobs; maximise bandwidth
//
// Pool pressure at 8 parallel workers = 8 × 1 MiB ≈ 8 MiB — negligible.

const (
	SmallBufSize  = 4 * 1024        // 4 KiB
	MediumBufSize = 64 * 1024       // 64 KiB
	LargeBufSize  = 1 * 1024 * 1024 // 1 MiB
	XLargeBufSize = 4 * 1024 * 1024 // 4 MiB
)

// BufPool is the interface for tiered buffer pools.
// Implementations must be safe for concurrent use.
type BufPool interface {
	// Get returns a buffer of at least size bytes.
	// The caller MUST call Put with the same pointer when done.
	Get(size int64) *[]byte
	// GetStream returns a 1 MiB buffer for streaming unknown-size reads.
	GetStream() *[]byte
	// Put returns a buffer to its originating tier.
	Put(b *[]byte)
}

// TieredPool is the canonical BufPool implementation backed by sync.Pool.
type TieredPool struct {
	small  sync.Pool
	medium sync.Pool
	large  sync.Pool
	xlarge sync.Pool
}

// NewTieredPool creates a TieredPool. Use DefaultPool for most callers.
func NewTieredPool() *TieredPool {
	make_ := func(sz int) func() any {
		return func() any { b := make([]byte, sz); return &b }
	}
	return &TieredPool{
		small:  sync.Pool{New: make_(SmallBufSize)},
		medium: sync.Pool{New: make_(MediumBufSize)},
		large:  sync.Pool{New: make_(LargeBufSize)},
		xlarge: sync.Pool{New: make_(XLargeBufSize)},
	}
}

// DefaultPool is the process-global buffer pool.
var DefaultPool = NewTieredPool()

// Get returns the SMALLEST tier that fits size. size ≤ 0 → small (empty file).
func (p *TieredPool) Get(size int64) *[]byte {
	switch {
	case size <= SmallBufSize:
		return p.small.Get().(*[]byte)
	case size <= MediumBufSize:
		return p.medium.Get().(*[]byte)
	case size <= LargeBufSize:
		return p.large.Get().(*[]byte)
	default:
		return p.xlarge.Get().(*[]byte)
	}
}

// GetStream returns a 1 MiB large buffer for streaming / unknown-size reads.
func (p *TieredPool) GetStream() *[]byte { return p.large.Get().(*[]byte) }

// Put returns b to its originating tier keyed by cap(*b).
func (p *TieredPool) Put(b *[]byte) {
	switch cap(*b) {
	case SmallBufSize:
		p.small.Put(b)
	case MediumBufSize:
		p.medium.Put(b)
	case LargeBufSize:
		p.large.Put(b)
	default: // xlarge or any out-of-band size
		p.xlarge.Put(b)
	}
}

// ── Digest helpers ─────────────────────────────────────────────────────────────

// MaxDigestSize covers all built-in algorithms:
// Blake3/SHA-512 = 64 B, SHA-256 = 32 B, xxHash = 8 B, CRC32C = 4 B.
const MaxDigestSize = 64

// DigestSink is a stack-allocated buffer for zero-heap h.Sum output (SKILL §5).
// Usage: var s DigestSink; d := s.Sum(h)
type DigestSink struct{ Buf [MaxDigestSize]byte }

// Sum appends the hash's current digest to the internal buffer and returns
// the slice. The slice is valid until the next Sum call on the same sink.
func (s *DigestSink) Sum(h interface{ Sum([]byte) []byte }) []byte {
	return h.Sum(s.Buf[:0])
}

// CloneDigest allocates and returns a persistent copy of d.
func CloneDigest(d []byte) []byte {
	cp := make([]byte, len(d))
	copy(cp, d)
	return cp
}

// ── Write helpers ──────────────────────────────────────────────────────────────

// MustWrite calls h.Write(p) and panics if it returns an error.
// hash.Hash.Write is specified to never return an error; this guards against
// broken implementations.
func MustWrite(h interface{ Write([]byte) (int, error) }, p []byte) {
	if _, err := h.Write(p); err != nil {
		panic("fshash/core: hash.Write: " + err.Error())
	}
}

// WriteString writes s to h with zero allocation via unsafe.Slice (SKILL §5).
func WriteString(h interface{ Write([]byte) (int, error) }, s string) {
	if len(s) == 0 {
		return
	}
	MustWrite(h, unsafe.Slice(unsafe.StringData(s), len(s))) //nolint:gosec
}
