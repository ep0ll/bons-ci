// Package chunk provides pluggable content hashing with buffer pooling.
// It supports multiple hash algorithms (SHA-256, BLAKE3, XXH3) behind a
// unified [Hasher] interface, enabling callers to select the appropriate
// trade-off between cryptographic strength and throughput.
package chunk

import (
	"crypto/sha256"
	"hash"
	"io"

	"github.com/zeebo/blake3"
	"github.com/zeebo/xxh3"
)

// Algorithm identifies a hash function.
type Algorithm string

const (
	// SHA256 is the standard cryptographic hash (32-byte output).
	SHA256 Algorithm = "sha256"

	// BLAKE3 is a high-speed cryptographic hash (32-byte output).
	// Recommended default for content-addressable storage.
	BLAKE3 Algorithm = "blake3"

	// XXH3 is a non-cryptographic hash (8-byte output).
	// Suitable only for cache keying and bloom filters, not content integrity.
	XXH3 Algorithm = "xxh3"
)

// Hasher computes content hashes using a specific algorithm.
type Hasher interface {
	// Hash returns the digest of the provided data.
	Hash(data []byte) []byte

	// HashReader reads all bytes from r and returns the digest.
	HashReader(r io.Reader) ([]byte, error)

	// Algorithm returns the identifier for this hash function.
	Algorithm() Algorithm

	// Size returns the output digest size in bytes.
	Size() int
}

// NewHasher creates a Hasher for the specified algorithm.
// Panics if the algorithm is not recognized.
func NewHasher(algo Algorithm) Hasher {
	switch algo {
	case SHA256:
		return &sha256Hasher{}
	case BLAKE3:
		return &blake3Hasher{}
	case XXH3:
		return &xxh3Hasher{}
	default:
		panic("chunk: unknown algorithm: " + string(algo))
	}
}

// ---------- SHA-256 ----------

type sha256Hasher struct{}

func (h *sha256Hasher) Hash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func (h *sha256Hasher) HashReader(r io.Reader) ([]byte, error) {
	w := sha256.New()
	if _, err := io.Copy(w, r); err != nil {
		return nil, err
	}
	return w.Sum(nil), nil
}

func (h *sha256Hasher) Algorithm() Algorithm { return SHA256 }
func (h *sha256Hasher) Size() int            { return sha256.Size }

// ---------- BLAKE3 ----------

type blake3Hasher struct{}

func (h *blake3Hasher) Hash(data []byte) []byte {
	hasher := blake3.New()
	hasher.Write(data)
	out := make([]byte, 32)
	hasher.Sum(out[:0])
	return out
}

func (h *blake3Hasher) HashReader(r io.Reader) ([]byte, error) {
	hasher := blake3.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return nil, err
	}
	out := make([]byte, 32)
	hasher.Sum(out[:0])
	return out, nil
}

func (h *blake3Hasher) Algorithm() Algorithm { return BLAKE3 }
func (h *blake3Hasher) Size() int            { return 32 }

// ---------- XXH3 ----------

type xxh3Hasher struct{}

func (h *xxh3Hasher) Hash(data []byte) []byte {
	sum := xxh3.Hash(data)
	return uint64ToBytes(sum)
}

func (h *xxh3Hasher) HashReader(r io.Reader) ([]byte, error) {
	hasher := xxh3.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return nil, err
	}
	return hasher.Sum(nil), nil
}

func (h *xxh3Hasher) Algorithm() Algorithm { return XXH3 }
func (h *xxh3Hasher) Size() int            { return 8 }

// uint64ToBytes converts a uint64 to an 8-byte big-endian slice.
func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
	return b
}

// NewHash creates a standard [hash.Hash] for the given algorithm.
// This is useful when incremental (streaming) hashing is needed.
func NewHash(algo Algorithm) hash.Hash {
	switch algo {
	case SHA256:
		return sha256.New()
	case BLAKE3:
		return blake3.New()
	case XXH3:
		return xxh3.New()
	default:
		panic("chunk: unknown algorithm: " + string(algo))
	}
}
