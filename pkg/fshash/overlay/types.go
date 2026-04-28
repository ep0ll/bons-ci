package overlay

import "github.com/bons/bons-ci/pkg/fshash/internal/core"

// EntryKind classifies how a path was affected by overlay metadata.
type EntryKind uint8

const (
	EntryRegular  EntryKind = iota // Normal file/dir, no overlay metadata
	EntryWhiteout                  // File deletion marker (.wh.<name>)
	EntryOpaque                    // Directory opacity marker (.wh..wh..opq)
	EntryCopyUp                    // File copied from lower layer for modification
)

// String returns a human-readable representation of EntryKind.
func (k EntryKind) String() string {
	switch k {
	case EntryRegular:
		return "regular"
	case EntryWhiteout:
		return "whiteout"
	case EntryOpaque:
		return "opaque"
	case EntryCopyUp:
		return "copy-up"
	default:
		return "unknown"
	}
}

// OverlayEntry represents a parsed overlay metadata interpretation of a path.
type OverlayEntry struct {
	RawPath    string       // Original path as seen on disk
	TargetPath string       // Resolved path (e.g. .wh.foo -> foo, or same as RawPath)
	Kind       EntryKind    // The kind of overlay metadata this entry represents
	LayerID    core.LayerID // The layer where this entry was observed
}

// MutationKind classifies the logical change caused by an overlay entry.
type MutationKind uint8

const (
	MutationModified MutationKind = iota // File created or modified
	MutationDeleted                      // File deleted (via whiteout)
	MutationOpaqued                      // Directory made opaque (via opaque marker)
	MutationRenamed                      // File renamed (via copy-up + whiteout pair)
)

// String returns a human-readable representation of MutationKind.
func (k MutationKind) String() string {
	switch k {
	case MutationModified:
		return "modified"
	case MutationDeleted:
		return "deleted"
	case MutationOpaqued:
		return "opaqued"
	case MutationRenamed:
		return "renamed"
	default:
		return "unknown"
	}
}

// Mutation represents a high-level filesystem change event emitted by the interpreter.
type Mutation struct {
	Kind       MutationKind // The logical modification kind
	Path       string       // The path affected (target path for whiteouts, dir path for opaques)
	LayerID    core.LayerID // The layer containing the mutation
	TargetPath string       // For renames: the new destination path (optional)
}
