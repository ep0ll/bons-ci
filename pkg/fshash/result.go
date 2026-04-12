package fshash

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// EntryKind classifies a filesystem entry.
type EntryKind uint8

const (
	KindFile    EntryKind = iota // regular file
	KindDir                      // directory
	KindSymlink                  // symbolic link
	KindOther                    // device, pipe, socket, …
)

func (k EntryKind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindDir:
		return "dir"
	case KindSymlink:
		return "symlink"
	default:
		return "other"
	}
}

// EntryResult holds the per-entry checksum produced during a walk.
type EntryResult struct {
	// RelPath is the slash-separated path relative to the root that was
	// passed to [Checksummer.Sum].
	RelPath string
	// Kind describes what kind of filesystem entry this is.
	Kind EntryKind
	// Digest is the raw digest bytes for this entry.
	Digest []byte
}

// Hex returns the hex-encoded digest.
func (e EntryResult) Hex() string { return hex.EncodeToString(e.Digest) }

func (e EntryResult) String() string {
	return fmt.Sprintf("%s  %s  (%s)", e.Hex(), e.RelPath, e.Kind)
}

// Result is the outcome of a [Checksummer.Sum] call.
type Result struct {
	// Digest is the root digest: for a file it is the file's digest; for a
	// directory it is derived from all descendants.
	Digest []byte
	// Entries holds one EntryResult per file/directory visited, in the
	// deterministic (sorted) order that was used to build the root digest.
	// It is populated only when [Options.CollectEntries] is true.
	Entries []EntryResult
}

// Hex returns the hex-encoded root digest.
func (r Result) Hex() string { return hex.EncodeToString(r.Digest) }

func (r Result) String() string { return r.Hex() }

// Equal reports whether r and other have identical root digests.
// Entry lists are not compared.
func (r Result) Equal(other Result) bool {
	if len(r.Digest) != len(other.Digest) {
		return false
	}
	for i, b := range r.Digest {
		if b != other.Digest[i] {
			return false
		}
	}
	return true
}

// ── Error types ───────────────────────────────────────────────────────────────

// String returns a human-readable summary of the diff.
func (d DiffResult) String() string {
	if d.Empty() {
		return "no differences"
	}
	parts := make([]string, 0, 3)
	if n := len(d.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("%d added", n))
	}
	if n := len(d.Removed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", n))
	}
	if n := len(d.Modified); n > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", n))
	}
	return strings.Join(parts, ", ")
}

// FileTooLargeError is returned by [Checksummer.Sum] when a file exceeds
// the [Options.SizeLimit] threshold.
type FileTooLargeError struct {
	Path  string
	Size  int64
	Limit int64
}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("fshash: file %q is %d bytes, exceeds limit %d", e.Path, e.Size, e.Limit)
}
