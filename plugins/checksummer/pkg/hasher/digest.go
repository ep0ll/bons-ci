//go:build linux

// hasher/digest.go – BLAKE3 hash backend using github.com/zeebo/blake3.
//
// github.com/zeebo/blake3 is a pure-Go BLAKE3 implementation with optional
// AVX-512/AVX2 acceleration via assembly when building with CPU feature tags.
// It implements hash.Hash so it is a drop-in replacement for any hash.Hash use.
//
// BLAKE3 is ~3× faster than SHA-256 on modern hardware and produces 32-byte
// digests by default (same size as SHA-256, simpler than SHA3-256).
package hasher

import (
	"hash"

	"github.com/zeebo/blake3"
)

// digestSize is the output size of BLAKE3 in bytes.
const digestSize = 32

// newDigest returns a fresh BLAKE3 hash.Hash ready to receive data.
func newDigest() hash.Hash {
	return blake3.New()
}

// emptyHash is the BLAKE3 digest of zero bytes, pre-computed at init.
// af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc5ca0b7c38f8398
var emptyHash = func() []byte {
	h := blake3.New()
	return h.Sum(nil)
}()

// EmptyHash returns a copy of the BLAKE3 digest of an empty byte sequence.
func EmptyHash() []byte {
	cp := make([]byte, len(emptyHash))
	copy(cp, emptyHash)
	return cp
}

// HashBytes hashes an in-memory slice with BLAKE3 and returns the raw digest.
func HashBytes(data []byte) []byte {
	if len(data) == 0 {
		return EmptyHash()
	}
	h := blake3.New()
	_, _ = h.Write(data)
	return h.Sum(nil)
}
