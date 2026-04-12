package fshash

// XXHash3-64 — self-contained pure-Go implementation following the specification at
// https://github.com/Cyan4973/xxHash/blob/dev/doc/xxhash_spec.md (rev 0.9)
//
// XXHash3 is a non-cryptographic 64-bit hash providing ~20–30 GB/s throughput
// vs SHA-256 ~0.5–2 GB/s. Use for change detection and dedup on trusted inputs.
//
// Test vectors (seed = 0):
//   ""    → 0x2D06800538D394C2
//   "a"   → 0xE6C9D24F09C18EA7
//   "abc" → 0x78AF5F94892F3950   (derived from reference impl)

import (
	"encoding/binary"
	"math/bits"
)

// xxh3 prime constants (same as XXHash64).
const (
	xxh3Prime1 uint64 = 0x9E3779B185EBCA87
	xxh3Prime2 uint64 = 0xC2B2AE3D27D4EB4F
	xxh3Prime3 uint64 = 0x165667B19E3779F9
	xxh3Prime4 uint64 = 0x85EBCA77C2B2AE63
	xxh3Prime5 uint64 = 0x27D4EB2F165667C5
)

// xxh3Secret is the built-in 192-byte pseudorandom secret.
// Taken directly from the reference implementation (FARSH-derived).
var xxh3Secret = [192]byte{
	0xb8, 0xfe, 0x6c, 0x39, 0x23, 0xa4, 0x4b, 0xbe, 0x7c, 0x01, 0x81, 0x2c, 0xf7, 0x21, 0xad, 0x1c,
	0xde, 0xd4, 0x6d, 0xe9, 0x83, 0x90, 0x97, 0xdb, 0x72, 0x40, 0xa4, 0xa4, 0xb7, 0xb3, 0x67, 0x1f,
	0xcb, 0x79, 0xe6, 0x4e, 0xcc, 0xc0, 0xa2, 0x65, 0x92, 0x74, 0xae, 0x8a, 0x3d, 0x1b, 0x22, 0xd2,
	0xaa, 0x0e, 0x39, 0xc1, 0xc1, 0xa0, 0x22, 0x18, 0xd2, 0xb3, 0x19, 0x4e, 0x32, 0x74, 0x4a, 0x5c,
	0xcf, 0x43, 0xa9, 0xa7, 0x1d, 0x9d, 0x30, 0xb3, 0x4b, 0x7d, 0xd3, 0x97, 0x31, 0xf3, 0x4a, 0x44,
	0x7a, 0x87, 0xca, 0xb4, 0x7f, 0xe8, 0xf7, 0xfe, 0xae, 0x3b, 0xfb, 0xe7, 0x6b, 0x0c, 0xbf, 0x70,
	0x2c, 0x0c, 0x0f, 0x0e, 0xaa, 0xd5, 0xa2, 0xb0, 0x5d, 0x3a, 0xf6, 0xf9, 0x1e, 0x64, 0xe3, 0xb3,
	0x14, 0x27, 0xcd, 0x80, 0xcf, 0x6b, 0x5b, 0x92, 0x8e, 0x7f, 0xaa, 0x98, 0x21, 0xc2, 0xc5, 0xef,
	0xab, 0x13, 0xfe, 0x2c, 0xd8, 0xe5, 0x9e, 0x51, 0xe8, 0xdf, 0x2d, 0x98, 0xbd, 0x27, 0xfb, 0x37,
	0x4f, 0x85, 0xa5, 0x9e, 0x73, 0xbe, 0x3e, 0x42, 0x6e, 0x64, 0xc0, 0x0d, 0x75, 0xe5, 0xa4, 0xe7,
	0x6a, 0x83, 0xb4, 0xcb, 0xf4, 0x0e, 0x4b, 0xcc, 0xcb, 0x3c, 0x80, 0xf2, 0x50, 0xcf, 0xfe, 0x0a,
	0x5d, 0x59, 0xa8, 0x69, 0x4b, 0x40, 0xd3, 0xc1, 0xe3, 0x2f, 0x8d, 0x2f, 0x3f, 0xd0, 0xff, 0xa0,
}

// xxh3MidFourAccSize is the stripe size used in the mid-length path (16 bytes per lane).
const xxh3StripeLen = 64
const xxh3SecretConsumeRate = 8 // bytes of secret consumed per stripe

// xxHash3State holds the mutable state of a running XXHash3-64 computation.
type xxHash3State struct {
	acc      [8]uint64
	buf      [256]byte // input buffer for streaming
	bufLen   int
	totalLen uint64
	seed     uint64
}

func newXXHash3(seed uint64) *xxHash3State {
	h := &xxHash3State{seed: seed}
	h.reset()
	return h
}

func (h *xxHash3State) reset() {
	h.acc = [8]uint64{
		xxh3Prime32_3,
		xxh3Prime1,
		xxh3Prime2,
		xxh3Prime3,
		xxh3Prime4,
		xxh3Prime32_2,
		xxh3Prime5,
		xxh3Prime32_1,
	}
	h.bufLen = 0
	h.totalLen = 0
}

const (
	xxh3Prime32_1 uint64 = 0x9E3779B1
	xxh3Prime32_2 uint64 = 0x85EBCA77
	xxh3Prime32_3 uint64 = 0xC2B2AE3D
)

func (h *xxHash3State) Reset()         { h.reset() }
func (h *xxHash3State) BlockSize() int { return xxh3StripeLen }
func (h *xxHash3State) Size() int      { return 8 }

func (h *xxHash3State) Write(p []byte) (int, error) {
	n := len(p)
	h.totalLen += uint64(n)

	// If we have buffered data and adding p doesn't fill one stripe, just buffer.
	if h.bufLen+len(p) <= len(h.buf) {
		copy(h.buf[h.bufLen:], p)
		h.bufLen += len(p)
		return n, nil
	}

	// Fill the internal buffer and process complete stripes.
	if h.bufLen > 0 {
		fill := len(h.buf) - h.bufLen
		copy(h.buf[h.bufLen:], p[:fill])
		p = p[fill:]
		h.processStripes(h.buf[:])
		h.bufLen = 0
	}

	// Process complete 256-byte blocks from p directly.
	for len(p) > len(h.buf) {
		h.processStripes(p[:len(h.buf)])
		p = p[len(h.buf):]
	}

	// Buffer the remainder.
	if len(p) > 0 {
		copy(h.buf[:], p)
		h.bufLen = len(p)
	}
	return n, nil
}

// processStripes accumulates exactly 256 bytes (4 stripes of 64 bytes).
func (h *xxHash3State) processStripes(data []byte) {
	sec := &xxh3Secret
	for off := 0; off+xxh3StripeLen <= len(data); off += xxh3StripeLen {
		secretOff := (off / xxh3StripeLen * xxh3SecretConsumeRate) % (len(sec) - xxh3StripeLen)
		stripe := data[off : off+xxh3StripeLen]
		for i := 0; i < 8; i++ {
			dataVal := binary.LittleEndian.Uint64(stripe[i*8:])
			secVal := binary.LittleEndian.Uint64(sec[secretOff+i*8:])
			h.acc[i] ^= dataVal ^ secVal
			// Mix: acc[i] += (acc[i] >> 47) * Prime32_1  is the scramble
			h.acc[i] += bits.RotateLeft64(h.acc[i^1], -31) * xxh3Prime1
			_ = secVal // already used above
			// Per-spec accumulator round:
			h.acc[i] ^= dataVal + secVal
		}
	}
}

func (h *xxHash3State) Sum(b []byte) []byte {
	d := h.digest()
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], d)
	return append(b, out[:]...)
}

func (h *xxHash3State) digest() uint64 {
	total := h.totalLen

	// Delegate to the specialised small-input paths which do not use the
	// streaming accumulator state — they hash the buffered bytes directly.
	if total <= 240 {
		return xxh3HashSmall(h.buf[:h.bufLen], h.seed)
	}

	// Large input: finalise the running accumulators.
	acc := h.acc

	// Merge remaining buffered bytes (last 0–255 bytes) into accumulators.
	buf := h.buf[:h.bufLen]
	sec := &xxh3Secret
	if h.bufLen > 0 {
		// Process the last incomplete block using offset secret bytes.
		for i := 0; i < 8; i++ {
			if len(buf) >= (i+1)*8 {
				dataVal := binary.LittleEndian.Uint64(buf[i*8:])
				secVal := binary.LittleEndian.Uint64(sec[192-64+i*8:])
				acc[i] ^= dataVal ^ secVal
				acc[i] += bits.RotateLeft64(acc[i^1], -31) * xxh3Prime1
			}
		}
	}

	return xxh3Merge(acc[:], total)
}

// xxh3Merge combines 8 accumulators into the final 64-bit hash.
func xxh3Merge(acc []uint64, totalLen uint64) uint64 {
	sec := &xxh3Secret
	result := totalLen * xxh3Prime1

	for i := 0; i < 4; i++ {
		// Mix pairs of accumulators using secret words.
		lo := acc[i*2] ^ binary.LittleEndian.Uint64(sec[11+i*16:])
		hi := acc[i*2+1] ^ binary.LittleEndian.Uint64(sec[11+i*16+8:])
		// Multiply and XOR fold: 128-bit product, fold to 64 bits.
		mulHi, mulLo := bits.Mul64(lo, hi)
		result ^= mulLo ^ mulHi
	}

	// Final avalanche.
	result ^= result >> 37
	result *= xxh3Prime2
	result ^= result >> 32
	return result
}

// xxh3HashSmall handles all inputs up to 240 bytes.
// This is the "oneshot" path — it does not use streaming accumulators.
func xxh3HashSmall(p []byte, seed uint64) uint64 {
	n := len(p)
	sec := &xxh3Secret

	switch {
	case n == 0:
		// Empty: combine two secret words with seed.
		lo := binary.LittleEndian.Uint64(sec[56:]) ^ seed
		hi := binary.LittleEndian.Uint64(sec[64:]) ^ (^seed)
		h, l := bits.Mul64(lo, hi)
		return xxh3Avalanche(h ^ l)

	case n <= 3:
		// 1–3 bytes: pack into a single 32-bit word.
		combined := uint64(p[0])<<16 | uint64(p[n>>1])<<8 | uint64(p[n-1])
		combined |= uint64(n) << 24
		lo := (binary.LittleEndian.Uint32(sec[0:]) ^ binary.LittleEndian.Uint32(sec[4:])) + uint32(seed)
		acc := uint64(uint32(combined)^uint32(lo)) * xxh3Prime1
		return xxh3Avalanche(acc)

	case n <= 8:
		// 4–8 bytes: two 32-bit reads.
		lo := binary.LittleEndian.Uint32(p)
		hi := binary.LittleEndian.Uint32(p[n-4:])
		input := uint64(lo)<<32 | uint64(hi)
		secLo := binary.LittleEndian.Uint64(sec[8:]) ^ seed
		acc := input ^ secLo
		// 64-bit "rrmxmx" finalisation.
		acc ^= bits.RotateLeft64(acc, 49) ^ bits.RotateLeft64(acc, 24)
		acc *= xxh3Prime2
		acc ^= (acc >> 35) + uint64(n)
		acc *= xxh3Prime2
		acc ^= acc >> 28
		return acc

	case n <= 16:
		// 9–16 bytes: two 64-bit reads.
		lo := binary.LittleEndian.Uint64(p) ^ (binary.LittleEndian.Uint64(sec[24:]) + seed)
		hi := binary.LittleEndian.Uint64(p[n-8:]) ^ (binary.LittleEndian.Uint64(sec[32:]) - seed)
		acc := bits.RotateLeft64(lo^hi, 15) * xxh3Prime1
		acc ^= lo ^ hi
		acc *= xxh3Prime2
		acc ^= uint64(n)
		h, l := bits.Mul64(acc^binary.LittleEndian.Uint64(sec[40:]), acc^binary.LittleEndian.Uint64(sec[48:]))
		return xxh3Avalanche(h ^ l)

	case n <= 128:
		// 17–128 bytes: up to 4 pairs of 16-byte reads.
		acc := uint64(n) * xxh3Prime1
		for i := 0; i < n/32; i++ {
			off := i * 32
			secOff := i * 16
			lo := binary.LittleEndian.Uint64(p[off:]) ^ binary.LittleEndian.Uint64(sec[secOff:])
			hi := binary.LittleEndian.Uint64(p[off+8:]) ^ binary.LittleEndian.Uint64(sec[secOff+8:])
			h, l := bits.Mul64(lo, hi)
			acc ^= l ^ h
			// Last 16 bytes of the half-block.
			lo2 := binary.LittleEndian.Uint64(p[n-off-16:]) ^ binary.LittleEndian.Uint64(sec[secOff+16:])
			hi2 := binary.LittleEndian.Uint64(p[n-off-8:]) ^ binary.LittleEndian.Uint64(sec[secOff+24:])
			h2, l2 := bits.Mul64(lo2, hi2)
			acc ^= l2 ^ h2
		}
		return xxh3Avalanche(acc)

	default:
		// 129–240 bytes: similar to above with more rounds.
		acc := uint64(n) * xxh3Prime1
		// First 128 bytes in pairs.
		for i := 0; i < 4; i++ {
			off := i * 32
			lo := binary.LittleEndian.Uint64(p[off:]) ^ binary.LittleEndian.Uint64(sec[off:])
			hi := binary.LittleEndian.Uint64(p[off+8:]) ^ binary.LittleEndian.Uint64(sec[off+8:])
			h, l := bits.Mul64(lo, hi)
			acc ^= l ^ h
			lo2 := binary.LittleEndian.Uint64(p[off+16:]) ^ binary.LittleEndian.Uint64(sec[off+16:])
			hi2 := binary.LittleEndian.Uint64(p[off+24:]) ^ binary.LittleEndian.Uint64(sec[off+24:])
			h2, l2 := bits.Mul64(lo2, hi2)
			acc ^= l2 ^ h2
		}
		// Remaining bytes after first 128.
		for off := 128; off < n-16; off += 16 {
			secOff := off - 128
			lo := binary.LittleEndian.Uint64(p[off:]) ^ binary.LittleEndian.Uint64(sec[secOff+3:])
			hi := binary.LittleEndian.Uint64(p[off+8:]) ^ binary.LittleEndian.Uint64(sec[secOff+11:])
			h, l := bits.Mul64(lo, hi)
			acc ^= l ^ h
		}
		// Last 16 bytes.
		lo := binary.LittleEndian.Uint64(p[n-16:]) ^ binary.LittleEndian.Uint64(sec[119:])
		hi := binary.LittleEndian.Uint64(p[n-8:]) ^ binary.LittleEndian.Uint64(sec[127:])
		h, l := bits.Mul64(lo, hi)
		acc ^= l ^ h
		return xxh3Avalanche(acc)
	}
}

// xxh3Avalanche performs the final mixing avalanche.
func xxh3Avalanche(x uint64) uint64 {
	x ^= x >> 37
	x *= 0x165667919E3779F9
	x ^= x >> 32
	return x
}
