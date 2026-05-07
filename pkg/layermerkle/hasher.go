package layermerkle

import (
	"context"
	"fmt"
	"os"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/hash"
	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
	"github.com/bons/bons-ci/pkg/layermerkle/internal/singleflight"
)

// ─────────────────────────────────────────────────────────────────────────────
// FileHasher interface
// ─────────────────────────────────────────────────────────────────────────────

// FileHasher computes the content-addressable digest of a file.
// Implementations must be safe for concurrent use.
type FileHasher interface {
	// Hash computes the digest of the file at absPath.
	// Returns ErrWhiteout when the path is an overlay whiteout entry.
	Hash(ctx context.Context, absPath string) (FileHash, error)

	// Algorithm returns the digest algorithm name (e.g. "sha256").
	Algorithm() string
}

// ─────────────────────────────────────────────────────────────────────────────
// SHA256Hasher — production FileHasher using pooled SHA-256
// ─────────────────────────────────────────────────────────────────────────────

// SHA256Hasher hashes file contents with SHA-256 using a pooled hash.Hash
// instance to eliminate per-file allocations on the hot path.
type SHA256Hasher struct{}

// NewSHA256Hasher returns a SHA256Hasher.
func NewSHA256Hasher() *SHA256Hasher { return &SHA256Hasher{} }

// Hash implements FileHasher.
func (h *SHA256Hasher) Hash(ctx context.Context, absPath string) (FileHash, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return hashFile(absPath)
}

// Algorithm implements FileHasher.
func (h *SHA256Hasher) Algorithm() string { return "sha256" }

func hashFile(absPath string) (FileHash, error) {
	f, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrWhiteout, absPath)
		}
		return "", fmt.Errorf("%w: open %s: %v", ErrHashFailed, absPath, err)
	}
	defer f.Close()

	sum, _, err := hash.SumFile(f, hash.SHA256Pool)
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %v", ErrHashFailed, absPath, err)
	}

	return digest.NewDigestFromBytes(digest.SHA256, sum), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SingleflightHasher — deduplicates concurrent requests for the same path
// ─────────────────────────────────────────────────────────────────────────────

// SingleflightHasher wraps a FileHasher so that concurrent calls for the same
// absPath are coalesced into a single I/O operation via singleflight.
// This is the Proxy + Flyweight pattern: identical requests share work.
type SingleflightHasher struct {
	inner FileHasher
	group singleflight.Group
}

// NewSingleflightHasher wraps inner so concurrent hashes of the same file
// collapse into one read.
func NewSingleflightHasher(inner FileHasher) *SingleflightHasher {
	return &SingleflightHasher{inner: inner}
}

// Hash implements FileHasher.
func (s *SingleflightHasher) Hash(ctx context.Context, absPath string) (FileHash, error) {
	v, err, _ := s.group.Do(absPath, func() (any, error) {
		return s.inner.Hash(ctx, absPath)
	})
	if err != nil {
		return "", err
	}
	return v.(FileHash), nil
}

// Algorithm implements FileHasher.
func (s *SingleflightHasher) Algorithm() string { return s.inner.Algorithm() }

// ─────────────────────────────────────────────────────────────────────────────
// HasherChain — strategy pattern for algorithm selection
// ─────────────────────────────────────────────────────────────────────────────

// HasherChain tries hashers in order, returning the first successful hash.
// Useful for migrating between algorithms: try the new hasher first, fall back
// to a legacy one for pre-existing cached files.
type HasherChain []FileHasher

// Hash implements FileHasher.
func (c HasherChain) Hash(ctx context.Context, absPath string) (FileHash, error) {
	var lastErr error
	for _, h := range c {
		d, err := h.Hash(ctx, absPath)
		if err == nil {
			return d, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// Algorithm returns the algorithm of the first hasher in the chain.
func (c HasherChain) Algorithm() string {
	if len(c) == 0 {
		return "none"
	}
	return c[0].Algorithm()
}
