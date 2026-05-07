package layermerkle

import (
	"fmt"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// Primitive type aliases — content-addressed identifiers
// ─────────────────────────────────────────────────────────────────────────────

// LayerID is the content-addressable digest of a single layer (lowerdir diff).
// Matches the digest produced by containerd / BuildKit when committing a snapshot.
type LayerID = digest.Digest

// VertexID is the content-addressable digest of an LLB ExecOp vertex.
// Events are grouped per vertex to build one MerkleTree per execution.
type VertexID = digest.Digest

// FileHash is the content-addressable digest of a file's contents.
type FileHash = digest.Digest

// LayerStack is an ordered slice of layer IDs representing a stacked overlay.
// Index 0 is the bottommost (oldest) layer; the last element is the topmost
// (most recent) layer added by the current ExecOp.
type LayerStack []LayerID

// Top returns the topmost (most recently added) layer, or the zero digest.
func (ls LayerStack) Top() LayerID {
	if len(ls) == 0 {
		return ""
	}
	return ls[len(ls)-1]
}

// Contains reports whether the stack includes the given layer.
func (ls LayerStack) Contains(id LayerID) bool {
	for _, l := range ls {
		if l == id {
			return true
		}
	}
	return false
}

// Valid reports whether the stack has at least one entry.
func (ls LayerStack) Valid() bool { return len(ls) > 0 }

// ─────────────────────────────────────────────────────────────────────────────
// AccessEvent — one file access received from the fanotify channel
// ─────────────────────────────────────────────────────────────────────────────

// AccessEvent is a file access record produced by the fanwatch pipeline and
// annotated with layer context before entering the deduplication engine.
//
// Consumers that source events from fanwatch's EnrichedEvent.Attrs should
// use [AccessEventFromAttrs] to decode them.
type AccessEvent struct {
	// VertexID is the ExecOp vertex that triggered this event.
	// All events sharing the same VertexID contribute to one MerkleTree.
	VertexID VertexID

	// LayerStack is the ordered overlay layer stack for this ExecOp,
	// bottommost (oldest) layer first.
	LayerStack LayerStack

	// RelPath is the file path relative to the overlay merged directory.
	// Always uses forward slashes and never starts with "/".
	RelPath string

	// AbsPath is the absolute path in the merged overlay view.
	AbsPath string

	// Mask captures the fanotify operation (ACCESS, OPEN, OPEN_EXEC, etc.).
	// Only read-class operations contribute to Merkle tree leaves.
	Mask uint64

	// PID is the kernel PID of the process that triggered the event.
	PID int32

	// Timestamp is when the event was observed by the fanotify watcher.
	Timestamp time.Time
}

// IsReadAccess reports whether this event represents a non-mutating file read.
// Only read-access events contribute to the Merkle tree; write events are ignored.
func (e *AccessEvent) IsReadAccess() bool {
	const readMask = 0x00000001 | 0x00000020 | 0x00001000 | 0x00000010
	return e.Mask&readMask != 0
}

// String implements fmt.Stringer for logging.
func (e *AccessEvent) String() string {
	return fmt.Sprintf("AccessEvent{vertex=%s top=%s path=%q pid=%d}",
		shortDigest(e.VertexID), shortDigest(e.LayerStack.Top()), e.RelPath, e.PID)
}

// ─────────────────────────────────────────────────────────────────────────────
// HashRequest — internal request to resolve a file hash
// ─────────────────────────────────────────────────────────────────────────────

// HashRequest is submitted to the DeduplicationEngine when a cache miss occurs.
// It carries all context needed to locate the file, compute its hash, and
// attribute the result to the correct owner layer.
type HashRequest struct {
	// VertexID groups the result into the correct MerkleTree.
	VertexID VertexID

	// LayerStack is the full overlay stack at the time of the access.
	LayerStack LayerStack

	// RelPath is the normalized relative file path.
	RelPath string

	// AbsPath is the absolute path in the merged view (for hashing).
	AbsPath string
}

// ─────────────────────────────────────────────────────────────────────────────
// HashResult — resolved hash + attribution
// ─────────────────────────────────────────────────────────────────────────────

// HashResult is produced by the deduplication engine for each unique
// (ownerLayer, relPath) pair. It carries the file hash and attribution so the
// MerkleTreeBuilder can insert the correct leaf.
type HashResult struct {
	// VertexID of the ExecOp that triggered this request.
	VertexID VertexID

	// RelPath is the file path relative to the merged directory.
	RelPath string

	// Hash is the content-addressable digest of the file.
	Hash FileHash

	// OwnerLayerID is the layer whose diff contains the file.
	// May differ from LayerStack.Top() when the file comes from a lower layer.
	OwnerLayerID LayerID

	// FromCache reports whether this result was served from the hash cache.
	// False means the FileHasher was invoked (cache miss).
	FromCache bool

	// Err is non-nil when the hash could not be computed (e.g., whiteout, I/O error).
	// A non-nil Err means this result is not added to the Merkle tree.
	Err error
}

// ─────────────────────────────────────────────────────────────────────────────
// Attr key constants — for fanwatch EnrichedEvent.Attrs integration
// ─────────────────────────────────────────────────────────────────────────────

// Attribute key names used when encoding layer context into fanwatch's Attrs map.
const (
	AttrVertexID   = "layermerkle.vertex.id"
	AttrLayerStack = "layermerkle.layer.stack" // colon-separated digest list
	AttrRelPath    = "layermerkle.rel.path"
)

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func shortDigest(d digest.Digest) string {
	s := d.String()
	if len(s) > 19 {
		return s[:19]
	}
	return s
}
