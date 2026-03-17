package diffview

// entry.go – the central vocabulary of a DiffView operation.
//
// A DiffEntry is produced by a Differ for every path it encounters during
// the comparison of lower and upper.  The Apply engine routes each entry:
//
//   - ActionDelete  →  Deleter.Delete(entry)  →  Observer.OnEvent
//   - ActionRetain  →  Observer.OnEvent  (no deletion performed)
//
// This file owns no logic — only types and their string representations.

import "fmt"

// ─── Action ───────────────────────────────────────────────────────────────────

// Action is the outcome that Apply should take for a given path.
type Action int8

const (
	// ActionDelete: path should be removed from the merged directory.
	ActionDelete Action = iota

	// ActionRetain: path was examined but must not be removed.
	ActionRetain
)

// ─── Reason ───────────────────────────────────────────────────────────────────

// DeleteReason explains why a path was selected for deletion.
// Valid only when Action == ActionDelete.
type DeleteReason int8

const (
	// DeleteReasonExclusiveLower: path exists in lower only (not in upper).
	// In the merged view this path was contributed by the base layer alone;
	// it is not part of the effective diff and must be removed.
	DeleteReasonExclusiveLower DeleteReason = iota

	// DeleteReasonCommonEqual: path exists in both lower and upper with
	// identical content (same SHA-256 or same metadata).
	// Upper did not modify it; the merged copy is redundant in the diff.
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

// RetainReason explains why a path was NOT deleted.
// Valid only when Action == ActionRetain.
type RetainReason int8

const (
	// RetainReasonCommonDifferent: path exists in both lower and upper with
	// different content.  Upper introduced a change; the merged copy is the
	// effective diff and must be preserved.
	RetainReasonCommonDifferent RetainReason = iota

	// RetainReasonHashError: content hashing failed (e.g. permission denied,
	// file vanished mid-walk).  The path is preserved as a safety measure.
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

// DiffEntry is a single item in the diff stream produced by a Differ.
//
// Every path encountered during the lower-vs-upper comparison becomes exactly
// one DiffEntry.  The Action field drives the Apply engine's routing decision.
//
// The merged-directory path is always MergedAbs — it is always set regardless
// of Action, enabling the Observer to report the full picture.
type DiffEntry struct {
	// RelPath is the path relative to all three roots (lower, upper, merged).
	RelPath string

	// MergedAbs is the absolute path of this entry inside the merged directory.
	// This is the path that is passed to Deleter.Delete when Action == ActionDelete.
	MergedAbs string

	// Action indicates what Apply should do with this entry.
	Action Action

	// IsDir is true when the entry is a directory.
	IsDir bool

	// Pruned is true when the entry is an exclusive directory whose entire
	// subtree is exclusive to lower.  The Deleter can use a single
	// os.RemoveAll call instead of per-file removal.
	// Only meaningful when Action == ActionDelete.
	Pruned bool

	// DeleteReason is set when Action == ActionDelete.
	DeleteReason DeleteReason

	// RetainReason is set when Action == ActionRetain.
	RetainReason RetainReason

	// Err is non-nil when the Differ encountered a per-entry error (e.g. hash
	// failure).  Action is always ActionRetain when Err != nil.
	Err error
}

// ShouldDelete reports whether this entry should be passed to the Deleter.
func (e DiffEntry) ShouldDelete() bool { return e.Action == ActionDelete }

// ShouldRetain reports whether this entry should be preserved in merged.
func (e DiffEntry) ShouldRetain() bool { return e.Action == ActionRetain }
