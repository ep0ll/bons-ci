// Package dedup implements the DedupEngine — the core deduplication logic
// that eliminates redundant hash computations across stacked filesystem layers.
//
// # How deduplication works
//
// Each file access event carries:
//   - FilePath: the absolute path within the merged overlay view
//   - LayerStack: ordered [base … output] layer digests
//   - AccessType: read | write | create | delete | rename | chmod
//
// For a READ event targeting output layer L_out:
//
//  1. Check cache[(filePath, L_out)] → hit: reuse, done.
//  2. Walk ancestors from L_out-1 down to base:
//     - hit (non-tombstone): promote hash to L_out, done.
//     - hit (tombstone): file deleted in this ancestor; stop walk.
//  3. Total miss: call HashProvider.Hash → store in cache[(filePath, L_out)].
//
// For a MUTATING event (write/create/chmod/rename):
//   - Always call HashProvider.Hash (bypass cache) → Set cache[(filePath, L_out)].
//   - This ensures a newer write never returns a stale lower-layer hash.
//
// For DELETE:
//   - Store a tombstone at (filePath, L_out).
//   - Future reads by higher layers will stop stack walking at this tombstone.
//
// # Singleflight
//
// Concurrent events with the same (filePath, outputLayer) key on a cache miss
// are collapsed into one HashProvider.Hash call using singleflight. Only
// one goroutine performs I/O; others share the result.
package dedup

import (
	"fmt"
	"time"

	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// ResultKind
// ─────────────────────────────────────────────────────────────────────────────

// ResultKind classifies the outcome of a dedup engine Process call.
type ResultKind uint8

const (
	// ResultCacheHit means the hash was reused from the cache without
	// calling the HashProvider. This is the happy path.
	ResultCacheHit ResultKind = iota

	// ResultCacheMiss means the hash was computed fresh by the HashProvider.
	ResultCacheMiss

	// ResultTombstone means the file was deleted in the output layer.
	// No hash is available. The file is not added to the Merkle tree.
	ResultTombstone

	// ResultSkipped means the event was a duplicate of an already-processed
	// event (same filePath, same outputLayer, idempotent re-delivery).
	ResultSkipped

	// ResultError means the HashProvider returned an error or the event
	// failed validation. The Error field is non-nil.
	ResultError
)

// String returns a human-readable label.
func (k ResultKind) String() string {
	switch k {
	case ResultCacheHit:
		return "cache_hit"
	case ResultCacheMiss:
		return "cache_miss"
	case ResultTombstone:
		return "tombstone"
	case ResultSkipped:
		return "skipped"
	case ResultError:
		return "error"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(k))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Result
// ─────────────────────────────────────────────────────────────────────────────

// Result is the output of a single DedupEngine.Process call.
//
// On success (Kind != ResultError), Hash is non-nil unless Kind == ResultTombstone.
// SourceLayer identifies which layer's cache entry was used.
type Result struct {
	// Event is a pointer to the original (unmodified) event.
	Event *event.FileAccessEvent

	// Kind classifies the outcome.
	Kind ResultKind

	// Hash is the file's content digest. Nil for tombstone and error results.
	Hash []byte

	// Algorithm is the hash algorithm, e.g., "sha256".
	Algorithm string

	// SourceLayer is the layer from which the hash was sourced.
	// For cache hits from a lower layer, this is that lower layer's digest.
	// For fresh computations, this equals Event.OutputLayer().
	SourceLayer layer.Digest

	// MerkleLeafAdded reports whether this result caused a leaf to be added
	// to the output layer's Merkle tree.
	MerkleLeafAdded bool

	// Error is non-nil only when Kind == ResultError.
	Error error

	// Duration is the wall-clock time spent processing this event, including
	// cache lookup, stack walking, and hash computation.
	Duration time.Duration
}

// IsSuccess reports whether the event was processed without error.
func (r Result) IsSuccess() bool {
	return r.Kind != ResultError
}

// String returns a compact, human-readable summary.
func (r Result) String() string {
	if r.Event == nil {
		return fmt.Sprintf("Result{kind=%s error=%v}", r.Kind, r.Error)
	}
	return fmt.Sprintf(
		"Result{kind=%s path=%q outputLayer=%s sourceLayer=%s dur=%s}",
		r.Kind, r.Event.FilePath, r.Event.OutputLayer(), r.SourceLayer, r.Duration,
	)
}
