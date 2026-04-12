package fshash

// xxHash64 — self-contained implementation following the specification at
// https://github.com/Cyan4973/xxHash/blob/dev/doc/xxhash_spec.md (rev 0.8)
//
// Test vectors (seed = 0):
//   ""    → 0xEF46DB3751D8E999
//   "a"   → 0xD24EC4F1A98C6E5B
//   "test" → see TestXXHash64_Vectors
//
// xxHash64 is NOT cryptographically secure.  Use it for deduplication,
// change detection, and non-security-sensitive integrity checks where raw
// throughput matters (typically 10–15 GB/s vs SHA-256 ~0.5–2 GB/s).

import (
	"encoding/binary"
	"math/bits"
)

// xxHash64 constants from the specification.
const (
	xxPrime1 uint64 = 0x9E3779B185EBCA87
	xxPrime2 uint64 = 0xC2B2AE3D27D4EB4F
	xxPrime3 uint64 = 0x165667B19E3779F9
	xxPrime4 uint64 = 0x85EBCA77C2B2AE63
	xxPrime5 uint64 = 0x27D4EB2F165667C5
)

// xxHash64State holds the mutable state of a running xxHash64 computation.
type xxHash64State struct {
	v1, v2, v3, v4 uint64
	buf            [32]byte // internal 32-byte buffer
	bufLen         int
	totalLen       uint64
	seed           uint64
}

func newXXHash64(seed uint64) *xxHash64State {
	h := &xxHash64State{seed: seed}
	h.reset()
	return h
}

func (h *xxHash64State) reset() {
	h.v1 = h.seed + xxPrime1 + xxPrime2
	h.v2 = h.seed + xxPrime2
	h.v3 = h.seed
	h.v4 = h.seed - xxPrime1
	h.bufLen = 0
	h.totalLen = 0
}

func (h *xxHash64State) Reset()         { h.reset() }
func (h *xxHash64State) BlockSize() int { return 32 }
func (h *xxHash64State) Size() int      { return 8 }

func (h *xxHash64State) Write(p []byte) (int, error) {
	n := len(p)
	h.totalLen += uint64(n)

	if h.bufLen+len(p) < 32 {
		copy(h.buf[h.bufLen:], p)
		h.bufLen += len(p)
		return n, nil
	}

	// Fill the internal buffer and process it.
	if h.bufLen > 0 {
		fill := 32 - h.bufLen
		copy(h.buf[h.bufLen:], p[:fill])
		h.processStripe(h.buf[:])
		h.bufLen = 0
		p = p[fill:]
	}

	// Process complete 32-byte stripes directly from p.
	for len(p) >= 32 {
		h.processStripe(p[:32])
		p = p[32:]
	}

	// Save the remainder.
	if len(p) > 0 {
		copy(h.buf[:], p)
		h.bufLen = len(p)
	}
	return n, nil
}

func (h *xxHash64State) processStripe(p []byte) {
	h.v1 = xxRound(h.v1, binary.LittleEndian.Uint64(p[0:]))
	h.v2 = xxRound(h.v2, binary.LittleEndian.Uint64(p[8:]))
	h.v3 = xxRound(h.v3, binary.LittleEndian.Uint64(p[16:]))
	h.v4 = xxRound(h.v4, binary.LittleEndian.Uint64(p[24:]))
}

// xxRound applies one accumulator round.
func xxRound(acc, input uint64) uint64 {
	acc += input * xxPrime2
	acc = bits.RotateLeft64(acc, 31)
	return acc * xxPrime1
}

// xxMergeRound merges one accumulator into the digest accumulator.
func xxMergeRound(acc, v uint64) uint64 {
	v = xxRound(0, v)
	acc ^= v
	return acc*xxPrime1 + xxPrime4
}

func (h *xxHash64State) Sum(b []byte) []byte {
	d := h.digest()
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], d)
	return append(b, out[:]...)
}

func (h *xxHash64State) digest() uint64 {
	var acc uint64

	if h.totalLen >= 32 {
		// Combine the four accumulators.
		acc = bits.RotateLeft64(h.v1, 1) +
			bits.RotateLeft64(h.v2, 7) +
			bits.RotateLeft64(h.v3, 12) +
			bits.RotateLeft64(h.v4, 18)
		acc = xxMergeRound(acc, h.v1)
		acc = xxMergeRound(acc, h.v2)
		acc = xxMergeRound(acc, h.v3)
		acc = xxMergeRound(acc, h.v4)
	} else {
		acc = h.seed + xxPrime5
	}

	acc += h.totalLen

	// Process remaining bytes in the internal buffer.
	p := h.buf[:h.bufLen]

	// 8-byte lane.
	for len(p) >= 8 {
		k1 := xxRound(0, binary.LittleEndian.Uint64(p))
		acc ^= k1
		acc = bits.RotateLeft64(acc, 27)*xxPrime1 + xxPrime4
		p = p[8:]
	}

	// 4-byte lane.
	if len(p) >= 4 {
		acc ^= uint64(binary.LittleEndian.Uint32(p)) * xxPrime1
		acc = bits.RotateLeft64(acc, 23)*xxPrime2 + xxPrime3
		p = p[4:]
	}

	// 1-byte lane.
	for _, v := range p {
		acc ^= uint64(v) * xxPrime5
		acc = bits.RotateLeft64(acc, 11) * xxPrime1
	}

	// Avalanche.
	acc ^= acc >> 33
	acc *= xxPrime2
	acc ^= acc >> 29
	acc *= xxPrime3
	acc ^= acc >> 32

	return acc
}
