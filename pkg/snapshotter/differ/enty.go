package diffview

// entry.go – domain vocabulary for a DiffView operation.
//
// DiffEntry answers a single question for each path encountered during an
// upper-vs-lower comparison:
//
//   "Should this path be deleted from the merged directory, and why?"
//
// Nothing in this file knows about dirsync internals, filesystems, or channels.
// It is pure Go value types that model the outcome of a classification decision.

import "fmt"

// ─── Action ───────────────────────────────────────────────────────────────────

// Action is the decision Apply takes for a given path in the merged directory.
type Action int8

const (
	// ActionDelete: remove the path from merged.
	ActionDelete Action = iota

	// ActionRetain: keep the path in merged unchanged.
	ActionRetain
)

func (a Action) String() string {
	switch a {
	case ActionDelete:
		return "delete"
	case ActionRetain:
		return "retain"
	default:
		return fmt.Sprintf("Action(%d)", int(a))
	}
}

// ─── DeleteReason ─────────────────────────────────────────────────────────────

// DeleteReason explains why a path is being deleted from merged.
// Only meaningful when Action == ActionDelete.
type DeleteReason int8

const (
	// DeleteReasonExclusiveLower: the path exists only in lower, not in upper.
	// It was contributed by the base layer and is absent from the effective
	// diff; the merged copy must be removed.
	DeleteReasonExclusiveLower DeleteReason = iota

	// DeleteReasonCommonEqual: the path exists in both lower and upper with
	// identical content (same hash or same metadata). Upper did not modify it;
	// the merged copy is redundant and should be removed.
	DeleteReasonCommonEqual
)

func (r DeleteReason) String() string {
	switch r {
	case DeleteReasonExclusiveLower:
		return "exclusive_lower"
	case DeleteReasonCommonEqual:
		return "common_equal"
	default:
		return fmt.Sprintf("DeleteReason(%d)", int(r))
	}
}

// ─── RetainReason ─────────────────────────────────────────────────────────────

// RetainReason explains why a path is being kept in merged.
// Only meaningful when Action == ActionRetain.
type RetainReason int8

const (
	// RetainReasonCommonDifferent: the path exists in both lower and upper
	// but with different content. Upper introduced a change; the merged copy
	// is the effective diff and must be preserved.
	RetainReasonCommonDifferent RetainReason = iota

	// RetainReasonHashError: content comparison failed (e.g. I/O error, file
	// disappeared mid-walk). The path is kept as a safety measure.
	RetainReasonHashError
)

func (r RetainReason) String() string {
	switch r {
	case RetainReasonCommonDifferent:
		return "common_different"
	case RetainReasonHashError:
		return "hash_error"
	default:
		return fmt.Sprintf("RetainReason(%d)", int(r))
	}
}

// ─── DiffEntry ────────────────────────────────────────────────────────────────

// DiffEntry is produced for every path examined during the lower-vs-upper
// comparison. It is the unit of observability in diffview: every DiffEntry
// becomes exactly one Observer.OnEvent call.
//
// Field validity matrix:
//
//	Field          ActionDelete          ActionRetain
//	──────────     ──────────────────    ──────────────────────
//	RelPath        always set            always set
//	MergedAbs      always set            always set
//	IsDir          always set            always set
//	Collapsed      set for dir subtrees  never set
//	DeleteReason   always set            zero (ignore)
//	RetainReason   zero (ignore)         always set
//	HashErr        never set             set on RetainReasonHashError
type DiffEntry struct {
	// RelPath is the forward-slash path relative to all three roots.
	RelPath string

	// MergedAbs is the absolute path of this entry inside the merged directory.
	MergedAbs string

	// Action is what Apply decided for this path.
	Action Action

	// IsDir is true when the entry is a directory.
	IsDir bool

	// Collapsed is true when the entry is an exclusive directory subtree that
	// dirsync has optimised into a single root entry. One os.RemoveAll /
	// io_uring unlinkat call is sufficient to remove the entire subtree.
	// Meaningful only when Action == ActionDelete && IsDir == true.
	Collapsed bool

	// DeleteReason is set when Action == ActionDelete.
	DeleteReason DeleteReason

	// RetainReason is set when Action == ActionRetain.
	RetainReason RetainReason

	// HashErr is non-nil when the hash comparison failed for this path.
	// Action is always ActionRetain when HashErr != nil.
	HashErr error
}

// IsCollapsedDir reports whether this entry can be removed with a single
// os.RemoveAll / io_uring unlinkat (AT_REMOVEDIR) call.
func (e DiffEntry) IsCollapsedDir() bool {
	return e.Action == ActionDelete && e.IsDir && e.Collapsed
}
