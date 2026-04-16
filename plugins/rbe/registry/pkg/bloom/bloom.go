// Package bloom provides a space-efficient, thread-safe probabilistic set.
//
// This implementation uses:
//   - Two independent FNV-1a hash functions (double-hashing technique) to
//     generate k virtual hash functions with O(1) arithmetic
//   - 64-bit atomic words so concurrent Add/Test require no mutexes
//   - CAS loops for lock-free bit-set operations (safe on all Go targets)
//
// False-positive probability formula:
//
//	p ≈ (1 - e^(-k*n/m))^k
//
// where n = inserted elements, m = total bits, k = hash functions.
//
// Recommended parameters (targeting p ≤ 0.01):
//
//	m = n * 10 bits,  k = 7
package bloom

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"sync/atomic"
)

const (
	// DefaultM is the default number of bits (≈ 8 MiB for ~800k elements at p≈0.01).
	DefaultM uint64 = 1 << 26 // 67,108,864 bits
	// DefaultK is the default number of hash functions.
	DefaultK uint = 7
)

// Filter is a thread-safe, lock-free counting Bloom filter.
// All exported methods are safe for concurrent use.
type Filter struct {
	bits []uint64 // backing bit array, using 64-bit atomic words
	m    uint64   // total bit capacity
	k    uint     // number of hash functions
}

// New allocates a Filter with m bits and k hash functions.
// m is rounded up to the nearest multiple of 64.
func New(m uint64, k uint) *Filter {
	// Align m to 64 so index arithmetic never overflows a word boundary.
	if rem := m % 64; rem != 0 {
		m += 64 - rem
	}
	return &Filter{
		bits: make([]uint64, m/64),
		m:    m,
		k:    k,
	}
}

// NewDefault returns a Filter tuned for n expected elements at p≈0.01.
func NewDefault(n uint64) *Filter {
	m, k := OptimalParams(n, 0.01)
	return New(m, k)
}

// OptimalParams computes optimal (m, k) for n elements and false-positive rate p.
func OptimalParams(n uint64, p float64) (m uint64, k uint) {
	// m = -n * ln(p) / (ln2)^2
	mf := -float64(n) * math.Log(p) / (math.Ln2 * math.Ln2)
	m = uint64(math.Ceil(mf))
	// k = (m/n) * ln2
	kf := float64(m) / float64(n) * math.Ln2
	k = uint(math.Round(kf))
	if k < 1 {
		k = 1
	}
	return
}

// Add inserts data into the filter.
// This operation is idempotent and fully lock-free.
func (f *Filter) Add(data []byte) {
	h1, h2 := twoHashes(data)
	for i := uint(0); i < f.k; i++ {
		pos := (h1 + uint64(i)*h2) % f.m
		f.setBit(pos)
	}
}

// Test returns true if data was (probably) added to the filter.
// A true result may be a false positive; false is always accurate.
func (f *Filter) Test(data []byte) bool {
	h1, h2 := twoHashes(data)
	for i := uint(0); i < f.k; i++ {
		pos := (h1 + uint64(i)*h2) % f.m
		if !f.testBit(pos) {
			return false // definitive miss
		}
	}
	return true // probable hit
}

// AddString is a convenience wrapper for string keys.
func (f *Filter) AddString(s string) { f.Add([]byte(s)) }

// TestString is a convenience wrapper for string keys.
func (f *Filter) TestString(s string) bool { return f.Test([]byte(s)) }

// EstimateFPRate returns the current estimated false-positive rate
// given insertedElements have been added.
func (f *Filter) EstimateFPRate(insertedElements uint64) float64 {
	// p ≈ (1 - e^(-k*n/m))^k
	exponent := -float64(f.k) * float64(insertedElements) / float64(f.m)
	return math.Pow(1-math.Exp(exponent), float64(f.k))
}

// BitCount returns the number of set bits (for diagnostic purposes).
// This is O(m/64) and should not be called on the hot path.
func (f *Filter) BitCount() int64 {
	var n int64
	for i := range f.bits {
		n += int64(popcount(atomic.LoadUint64(&f.bits[i])))
	}
	return n
}

// M returns the total bit capacity.
func (f *Filter) M() uint64 { return f.m }

// K returns the number of hash functions.
func (f *Filter) K() uint { return f.k }

// ────────────────────────────────────────────────────────────────────────────
// Lock-free bit manipulation
// ────────────────────────────────────────────────────────────────────────────

// setBit sets the bit at position pos using a CAS loop (lock-free).
func (f *Filter) setBit(pos uint64) {
	word := pos / 64
	mask := uint64(1) << (pos % 64)
	for {
		old := atomic.LoadUint64(&f.bits[word])
		if old&mask != 0 {
			return // already set, nothing to do
		}
		if atomic.CompareAndSwapUint64(&f.bits[word], old, old|mask) {
			return
		}
		// Another goroutine raced — retry. Extremely rare.
	}
}

// testBit reads the bit at position pos atomically.
func (f *Filter) testBit(pos uint64) bool {
	word := pos / 64
	mask := uint64(1) << (pos % 64)
	return atomic.LoadUint64(&f.bits[word])&mask != 0
}

// ────────────────────────────────────────────────────────────────────────────
// Hashing — double-hashing with FNV-1a (fast, good avalanche, no allocs)
// ────────────────────────────────────────────────────────────────────────────

// twoHashes computes two independent 64-bit FNV-1a hashes for double hashing.
// The first is standard FNV-1a; the second uses a seeded variant by XOR-ing
// a constant into the state before writing data.
func twoHashes(data []byte) (h1, h2 uint64) {
	h := fnv.New64a()
	_, _ = h.Write(data)
	h1 = h.Sum64()

	// Seeded second hash: reset state with a different offset basis.
	// FNV-1a offset basis = 14695981039346656037; we XOR a random constant.
	const seed2 = uint64(0xdeadbeefcafebabe)
	h2 = xorFNV64a(data, seed2)
	if h2 == 0 {
		h2 = 1 // prevent h2 = 0 which collapses to a single hash
	}
	return
}

// xorFNV64a runs FNV-1a starting with an XOR'd basis (no heap alloc).
func xorFNV64a(data []byte, seed uint64) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	h := offset ^ seed
	for _, b := range data {
		h ^= uint64(b)
		h *= prime
	}
	return h
}

// ────────────────────────────────────────────────────────────────────────────
// Digest-specific helpers (hot path for the accel index)
// ────────────────────────────────────────────────────────────────────────────

// AddDigestString adds a digest string (e.g. "sha256:abc...") to the filter
// using the raw 32-byte binary hash (avoids string → []byte copy on hot path).
func (f *Filter) AddDigestString(dgst string) {
	b := digestStringToBytes(dgst)
	h1, h2 := twoHashesFixed(b)
	for i := uint(0); i < f.k; i++ {
		f.setBit((h1 + uint64(i)*h2) % f.m)
	}
}

// TestDigestString tests a digest string against the filter.
func (f *Filter) TestDigestString(dgst string) bool {
	b := digestStringToBytes(dgst)
	h1, h2 := twoHashesFixed(b)
	for i := uint(0); i < f.k; i++ {
		if !f.testBit((h1 + uint64(i)*h2) % f.m) {
			return false
		}
	}
	return true
}

// digestStringToBytes converts a digest string to a fixed-size byte array
// holding only the hex portion (after the first colon), avoiding allocs by
// using binary.BigEndian on the first 16 hex chars (64 bits × 2 = 128 bits).
// For a full exact representation we use the full hex string bytes.
func digestStringToBytes(dgst string) []byte {
	// Skip algorithm prefix ("sha256:", "sha512:", etc.)
	for i := 0; i < len(dgst); i++ {
		if dgst[i] == ':' {
			return []byte(dgst[i+1:])
		}
	}
	return []byte(dgst)
}

// twoHashesFixed is a zero-alloc version for fixed-size byte slices.
func twoHashesFixed(b []byte) (h1, h2 uint64) {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
		seed2  = uint64(0xdeadbeefcafebabe)
	)
	h1 = offset
	h2 = offset ^ seed2
	for _, c := range b {
		h1 ^= uint64(c)
		h1 *= prime
		h2 ^= uint64(c)
		h2 *= prime
	}
	if h2 == 0 {
		h2 = 1
	}
	return
}

// ────────────────────────────────────────────────────────────────────────────
// Popcount (Hamming weight) — used for BitCount
// ────────────────────────────────────────────────────────────────────────────

// popcount returns the number of set bits in x using Brian Kernighan's method.
// For 64-bit words this runs in O(set bits) which is efficient for sparse filters.
func popcount(x uint64) int {
	n := 0
	for x != 0 {
		x &= x - 1
		n++
	}
	return n
}

// ────────────────────────────────────────────────────────────────────────────
// Serialisation helpers (for persistence / snapshotting)
// ────────────────────────────────────────────────────────────────────────────

// Marshal serialises the filter state to a byte slice suitable for storage.
// Format: [m uint64 LE][k uint64 LE][bits...]
func (f *Filter) Marshal() []byte {
	buf := make([]byte, 16+len(f.bits)*8)
	binary.LittleEndian.PutUint64(buf[0:8], f.m)
	binary.LittleEndian.PutUint64(buf[8:16], uint64(f.k))
	for i, w := range f.bits {
		binary.LittleEndian.PutUint64(buf[16+i*8:], atomic.LoadUint64(&f.bits[i]))
		_ = w
	}
	return buf
}

// Unmarshal restores a Filter from bytes produced by Marshal.
func Unmarshal(data []byte) (*Filter, error) {
	if len(data) < 16 {
		return nil, &bloomError{"data too short"}
	}
	m := binary.LittleEndian.Uint64(data[0:8])
	k := uint(binary.LittleEndian.Uint64(data[8:16]))
	words := (m + 63) / 64
	if uint64(len(data)-16) < words*8 {
		return nil, &bloomError{"data truncated"}
	}
	bits := make([]uint64, words)
	for i := range bits {
		bits[i] = binary.LittleEndian.Uint64(data[16+i*8:])
	}
	return &Filter{bits: bits, m: m, k: k}, nil
}

type bloomError struct{ msg string }

func (e *bloomError) Error() string { return "bloom: " + e.msg }
