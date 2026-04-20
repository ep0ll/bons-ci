// Package hasher provides BLAKE3-based file hashing with multiple strategies.
//
// BLAKE3 dependency
// ─────────────────
// The engine is designed for github.com/zeebo/blake3 (pure-Go BLAKE3).
// To enable it, add the module and swap the build tag:
//
//	go get github.com/zeebo/blake3
//
// Then change the file blake3_backend.go build constraint from:
//	//go:build !blake3
// to:
//	//go:build blake3
//
// And change blake3_sha256stub.go from:
//	//go:build blake3
// to:
//	//go:build !blake3
//
// By default the engine ships with a SHA-256 stub so it compiles without any
// external dependencies.  The stub produces correct, deterministic digests but
// SHA-256 is ~3× slower than BLAKE3 on modern hardware.
//
// Strategies
// ──────────
//   - Blake3Hasher   sequential pread64  (small files, HDD)
//   - MmapHasher     mmap + MADV_SEQUENTIAL (medium files, hot page cache)
//   - ParallelHasher concurrent pread64   (large files, NVMe multi-queue)
//   - AdaptiveHasher selects strategy by file size (recommended default)
package hasher

import (
	"context"
	"io"
)

// ─────────────────────────── Hasher interface ─────────────────────────────────

// Hasher computes a cryptographic digest of file contents.
// All implementations are safe for concurrent use.
type Hasher interface {
	// HashFile hashes the named file. Returns a 32-byte BLAKE3 digest.
	HashFile(ctx context.Context, path string) ([]byte, error)

	// HashFD hashes an open file descriptor (no close).
	// size is the file length; pass -1 to have it fstat'd internally.
	HashFD(ctx context.Context, fd int, size int64) ([]byte, error)

	// HashReader hashes all remaining bytes from r.
	HashReader(ctx context.Context, r io.Reader) ([]byte, error)
}

// Strategy identifies which hashing path was taken.
type Strategy uint8

const (
	StrategySequential Strategy = iota
	StrategyMmap
	StrategyParallel
)

func (s Strategy) String() string {
	switch s {
	case StrategySequential:
		return "sequential"
	case StrategyMmap:
		return "mmap"
	case StrategyParallel:
		return "parallel"
	}
	return "unknown"
}

// Result is the outcome of a hash operation.
type Result struct {
	Hash     []byte
	Size     int64
	Strategy Strategy
}

// compile-time interface checks
var (
	_ Hasher = (*Blake3Hasher)(nil)
	_ Hasher = (*MmapHasher)(nil)
	_ Hasher = (*ParallelHasher)(nil)
	_ Hasher = (*AdaptiveHasher)(nil)
)
