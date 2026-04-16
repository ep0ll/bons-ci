// Package core provides low-level, reusable primitives for content hashing.
// It has no filesystem dependencies and can be imported by any package.
//
// External libraries used (no reinvented wheels):
//   - github.com/zeebo/blake3    — BLAKE3-256, ~8× faster than SHA-256
//   - github.com/cespare/xxhash/v2 — XXHash64, ~15 GB/s
//   - github.com/zeebo/xxh3       — XXHash3-64, ~25 GB/s
//   - hash/crc32 (stdlib)          — CRC32C with hardware SSE4.2 acceleration
package core

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"sync"

	xxhash "github.com/cespare/xxhash/v2"
	"github.com/zeebo/blake3"
	"github.com/zeebo/xxh3"
)

// ── Algorithm ──────────────────────────────────────────────────────────────────

// Algorithm is a named hash algorithm identifier.
// Custom algorithms can be registered via Registry.Register.
type Algorithm string

// Built-in algorithms.
const (
	SHA256   Algorithm = "sha256"
	SHA512   Algorithm = "sha512"
	SHA1     Algorithm = "sha1"
	MD5      Algorithm = "md5"
	XXHash64 Algorithm = "xxhash64"
	XXHash3  Algorithm = "xxhash3"  // 64-bit; ~2× faster than xxhash64
	Blake3   Algorithm = "blake3"   // 256-bit crypto; ~8× faster than SHA-256
	CRC32C   Algorithm = "crc32c"   // 32-bit; hardware SSE4.2 accelerated
)

// ── Hasher interface ───────────────────────────────────────────────────────────

// Hasher is a factory for hash.Hash instances.
// All implementations MUST be safe for concurrent use.
type Hasher interface {
	// New returns a fresh, zeroed hash.Hash.
	New() hash.Hash
	// Algorithm returns the canonical algorithm name.
	Algorithm() string
	// DigestSize is the output size in bytes.
	DigestSize() int
}

// ── stdHasher ─────────────────────────────────────────────────────────────────

type stdHasher struct {
	algo    Algorithm
	newFn   func() hash.Hash
	dgstSz  int
}

func (s *stdHasher) New() hash.Hash    { return s.newFn() }
func (s *stdHasher) Algorithm() string { return string(s.algo) }
func (s *stdHasher) DigestSize() int   { return s.dgstSz }

// ── CRC32C wrapper ────────────────────────────────────────────────────────────
// CRC32C via stdlib uses SSE4.2 CLMUL when available — zero extra cost.

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

func newCRC32C() hash.Hash { return crc32.New(crc32cTable) }

// ── XXHash64 wrapper ──────────────────────────────────────────────────────────
// cespare/xxhash already implements hash.Hash; wrap to ensure big-endian Sum.

type xxhash64Wrapper struct{ *xxhash.Digest }

func (w *xxhash64Wrapper) Sum(b []byte) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], w.Sum64())
	return append(b, out[:]...)
}

func newXXHash64() hash.Hash { return &xxhash64Wrapper{xxhash.New()} }

// ── XXHash3 wrapper ───────────────────────────────────────────────────────────
// zeebo/xxh3 implements hash.Hash natively; wrap for big-endian Sum output.

type xxh3Wrapper struct{ *xxh3.Hasher }

func (w *xxh3Wrapper) Sum(b []byte) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], w.Sum64())
	return append(b, out[:]...)
}

func newXXHash3() hash.Hash { return &xxh3Wrapper{xxh3.New()} }

// ── Blake3 wrapper ────────────────────────────────────────────────────────────
// zeebo/blake3.New() returns *blake3.Digest which implements hash.Hash.

func newBlake3() hash.Hash { return blake3.New() }

// ── Registry ──────────────────────────────────────────────────────────────────

// Registry maps algorithm names to Hasher factories.
// It is safe for concurrent reads after initial setup.
// Use Register to add custom algorithms before first use.
type Registry struct {
	mu      sync.RWMutex
	hashers map[Algorithm]Hasher
}

// NewRegistry returns an empty registry.
// Prefer DefaultRegistry for most use cases.
func NewRegistry() *Registry { return &Registry{hashers: make(map[Algorithm]Hasher)} }

// Register adds or replaces a Hasher for the given algorithm.
// Thread-safe; may be called at init time.
func (r *Registry) Register(algo Algorithm, h Hasher) {
	r.mu.Lock()
	r.hashers[algo] = h
	r.mu.Unlock()
}

// Get returns the Hasher for algo, or an error if unknown.
func (r *Registry) Get(algo Algorithm) (Hasher, error) {
	r.mu.RLock()
	h, ok := r.hashers[algo]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("fshash/core: unknown algorithm %q (registered: %v)", algo, r.Algorithms())
	}
	return h, nil
}

// MustGet is like Get but panics on error.
func (r *Registry) MustGet(algo Algorithm) Hasher {
	h, err := r.Get(algo)
	if err != nil {
		panic(err)
	}
	return h
}

// Algorithms returns the sorted list of registered algorithm names.
func (r *Registry) Algorithms() []Algorithm {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Algorithm, 0, len(r.hashers))
	for k := range r.hashers {
		out = append(out, k)
	}
	return out
}

// ── DefaultRegistry ───────────────────────────────────────────────────────────

// DefaultRegistry is the global algorithm registry pre-loaded with all
// built-in algorithms. Call DefaultRegistry.Register to add custom ones.
var DefaultRegistry = func() *Registry {
	r := NewRegistry()
	r.Register(SHA256,   &stdHasher{algo: SHA256,   newFn: sha256.New,    dgstSz: 32})
	r.Register(SHA512,   &stdHasher{algo: SHA512,   newFn: sha512.New,    dgstSz: 64})
	r.Register(SHA1,     &stdHasher{algo: SHA1,     newFn: sha1.New,      dgstSz: 20})  //nolint:gosec
	r.Register(MD5,      &stdHasher{algo: MD5,      newFn: md5.New,       dgstSz: 16})  //nolint:gosec
	r.Register(XXHash64, &stdHasher{algo: XXHash64, newFn: newXXHash64,   dgstSz: 8})
	r.Register(XXHash3,  &stdHasher{algo: XXHash3,  newFn: newXXHash3,    dgstSz: 8})
	r.Register(Blake3,   &stdHasher{algo: Blake3,   newFn: newBlake3,     dgstSz: 32})
	r.Register(CRC32C,   &stdHasher{algo: CRC32C,   newFn: newCRC32C,     dgstSz: 4})
	return r
}()

// NewHasher is a convenience wrapper around DefaultRegistry.Get.
func NewHasher(algo Algorithm) (Hasher, error) { return DefaultRegistry.Get(algo) }

// MustHasher is like NewHasher but panics on error.
func MustHasher(algo Algorithm) Hasher { return DefaultRegistry.MustGet(algo) }
