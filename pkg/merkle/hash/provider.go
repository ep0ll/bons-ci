// Package hash defines the HashProvider strategy interface for computing
// file content hashes, along with built-in implementations and the request/
// result envelope types used throughout layermerkle.
//
// The HashProvider is the only I/O boundary in the layermerkle package.
// All other components are pure in-memory operations. Callers supply a
// HashProvider that understands their filesystem layout (e.g., a merged
// overlay path, a content-addressed store, or a test stub).
package hash

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// Algorithms
// ─────────────────────────────────────────────────────────────────────────────

// Algorithm identifies the hash algorithm used.
type Algorithm string

const (
	AlgorithmSHA256 Algorithm = "sha256"
	AlgorithmSHA512 Algorithm = "sha512"
)

// String returns the algorithm label.
func (a Algorithm) String() string { return string(a) }

// ─────────────────────────────────────────────────────────────────────────────
// HashRequest
// ─────────────────────────────────────────────────────────────────────────────

// HashRequest is the input to a HashProvider.Hash call. It carries all
// context needed to locate and hash the correct version of the file.
//
// The provider receives the full LayerStack so it can resolve the file from
// the appropriate overlay layer. The OutputLayer field is always equal to
// LayerStack.Top() and is provided as a convenience.
type HashRequest struct {
	// FilePath is the absolute path within the merged filesystem view.
	FilePath string

	// LayerStack is ordered from lowest (oldest base) to highest (output).
	LayerStack layer.Stack

	// OutputLayer is LayerStack.Top() — provided for convenience.
	OutputLayer layer.Digest
}

// ─────────────────────────────────────────────────────────────────────────────
// HashResult
// ─────────────────────────────────────────────────────────────────────────────

// HashResult is the output of a HashProvider.Hash call.
type HashResult struct {
	// Hash is the raw digest bytes. Never nil on success.
	Hash []byte

	// Hex is the hex-encoded digest string (for logging and display).
	Hex string

	// Algorithm identifies the hash function used.
	Algorithm Algorithm

	// SourceLayer is the layer the file content was actually read from.
	// For files that exist in the output layer (written/created by the
	// current ExecOp), SourceLayer == OutputLayer. For files inherited
	// from lower layers, SourceLayer is one of the ancestor layers.
	//
	// This field may be left empty if the provider cannot determine
	// provenance; the dedup engine will infer it from the cache.
	SourceLayer layer.Digest
}

// Equal reports whether two HashResults have identical digests.
func (r HashResult) Equal(other HashResult) bool {
	if len(r.Hash) != len(other.Hash) {
		return false
	}
	for i := range r.Hash {
		if r.Hash[i] != other.Hash[i] {
			return false
		}
	}
	return true
}

// String returns the hex digest for display.
func (r HashResult) String() string {
	if r.Hex != "" {
		return string(r.Algorithm) + ":" + r.Hex
	}
	return "<empty>"
}

// ─────────────────────────────────────────────────────────────────────────────
// HashProvider interface
// ─────────────────────────────────────────────────────────────────────────────

// HashProvider is the strategy interface for computing a file's content hash.
//
// Implementations must be:
//   - Goroutine-safe: Hash may be called from multiple goroutines concurrently.
//   - Deterministic: the same file content must always produce the same hash.
//   - Context-aware: implementations must honour context cancellation.
//
// The dedup engine calls Hash only on cache misses, so implementations need
// not implement their own caching.
type HashProvider interface {
	// Hash computes the content hash for the file described in req.
	// Returning an error causes the event to be marked as failed but does
	// not terminate the pipeline.
	Hash(ctx context.Context, req HashRequest) (HashResult, error)

	// Algorithm returns the algorithm label produced by this provider.
	Algorithm() Algorithm
}

// ─────────────────────────────────────────────────────────────────────────────
// SHA256Provider
// ─────────────────────────────────────────────────────────────────────────────

// SHA256Provider is a concrete HashProvider that reads the file at the given
// path using a caller-supplied ReadFunc and computes its SHA256 digest.
//
// ReadFunc is a function that opens and reads the file content. This
// indirection keeps the provider testable and decoupled from `os` package
// calls. In production, supply ReadFile(filePath string) ([]byte, error).
type SHA256Provider struct {
	readFunc ReadFunc
}

// ReadFunc is the file-reading strategy for SHA256Provider.
// It receives the absolute merged-view path and returns the file content.
type ReadFunc func(ctx context.Context, filePath string, stack layer.Stack) ([]byte, error)

// NewSHA256Provider creates a SHA256Provider that reads files using readFunc.
func NewSHA256Provider(readFunc ReadFunc) *SHA256Provider {
	if readFunc == nil {
		panic("hash: NewSHA256Provider: readFunc must not be nil")
	}
	return &SHA256Provider{readFunc: readFunc}
}

// Hash reads the file and returns its SHA256 digest.
func (p *SHA256Provider) Hash(ctx context.Context, req HashRequest) (HashResult, error) {
	if req.FilePath == "" {
		return HashResult{}, fmt.Errorf("hash: empty file path")
	}
	data, err := p.readFunc(ctx, req.FilePath, req.LayerStack)
	if err != nil {
		return HashResult{}, fmt.Errorf("hash: reading %q: %w", req.FilePath, err)
	}
	sum := sha256.Sum256(data)
	raw := sum[:]
	return HashResult{
		Hash:        raw,
		Hex:         hex.EncodeToString(raw),
		Algorithm:   AlgorithmSHA256,
		SourceLayer: req.OutputLayer, // caller may override after the fact
	}, nil
}

// Algorithm implements HashProvider.
func (p *SHA256Provider) Algorithm() Algorithm { return AlgorithmSHA256 }

// ─────────────────────────────────────────────────────────────────────────────
// SyntheticProvider (for testing / simulation)
// ─────────────────────────────────────────────────────────────────────────────

// SyntheticProvider returns a deterministic hash derived from the file path
// and the topmost layer digest. This provider performs no I/O and is intended
// for unit tests, integration tests, and simulation harnesses.
//
// The synthetic hash is: sha256(filePath || "|" || outputLayer)
type SyntheticProvider struct{}

// NewSyntheticProvider creates a SyntheticProvider.
func NewSyntheticProvider() *SyntheticProvider { return &SyntheticProvider{} }

// Hash computes a synthetic deterministic hash.
func (p *SyntheticProvider) Hash(_ context.Context, req HashRequest) (HashResult, error) {
	if req.FilePath == "" {
		return HashResult{}, fmt.Errorf("hash: empty file path")
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s", req.FilePath, req.OutputLayer)
	raw := h.Sum(nil)
	return HashResult{
		Hash:        raw,
		Hex:         hex.EncodeToString(raw),
		Algorithm:   AlgorithmSHA256,
		SourceLayer: req.OutputLayer,
	}, nil
}

// Algorithm implements HashProvider.
func (p *SyntheticProvider) Algorithm() Algorithm { return AlgorithmSHA256 }

// Compile-time assertions that both providers satisfy HashProvider.
var (
	_ HashProvider = (*SHA256Provider)(nil)
	_ HashProvider = (*SyntheticProvider)(nil)
)
