// Package fshash computes reproducible, hermetic checksums of files and
// directory trees.
//
// Architecture — two layers:
//
//	pkg/fshash/core   Low-level, dependency-free primitives (algorithms,
//	                  buffer pools, worker pools, reactive streams). Reusable
//	                  by any external package.
//
//	pkg/fshash        High-level filesystem API: Checksummer, Snapshot,
//	                  Watcher, CompareTrees, Walk, Canonicalize, SumMany.
//
// External libraries used (no reinvented wheels):
//
//	github.com/zeebo/blake3       BLAKE3-256, ~8× faster than SHA-256
//	github.com/cespare/xxhash/v2  XXHash64, ~15 GB/s
//	github.com/zeebo/xxh3         XXHash3-64, ~25 GB/s
//	hash/crc32 (stdlib)           CRC32C with SSE4.2 hardware acceleration
package fshash

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// ── EntryKind ──────────────────────────────────────────────────────────────────

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

// ── EntryResult ───────────────────────────────────────────────────────────────

// EntryResult holds the per-entry checksum produced during a walk.
type EntryResult struct {
	RelPath string    // slash-separated path relative to Sum root
	Kind    EntryKind
	Digest  []byte
}

func (e EntryResult) Hex() string    { return hex.EncodeToString(e.Digest) }
func (e EntryResult) String() string { return fmt.Sprintf("%s  %s  (%s)", e.Hex(), e.RelPath, e.Kind) }

// ── Result ────────────────────────────────────────────────────────────────────

// Result is the outcome of a Checksummer.Sum call.
type Result struct {
	Digest  []byte        // root digest
	Entries []EntryResult // populated only when CollectEntries is true
}

func (r Result) Hex() string    { return hex.EncodeToString(r.Digest) }
func (r Result) String() string { return r.Hex() }

// Equal reports whether r and other have identical root digests.
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

// ── DiffResult ────────────────────────────────────────────────────────────────

// DiffResult describes entry-level differences between two trees.
type DiffResult struct {
	Added    []string
	Removed  []string
	Modified []string
}

func (d DiffResult) Empty() bool {
	return len(d.Added)+len(d.Removed)+len(d.Modified) == 0
}

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

// ── Error types ───────────────────────────────────────────────────────────────

// VerifyError is returned by Checksummer.Verify on a digest mismatch.
type VerifyError struct {
	Path string
	Got  []byte
	Want []byte
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("fshash: verify %q: got %x, want %x", e.Path, e.Got, e.Want)
}

// FileTooLargeError is returned when a file exceeds Options.SizeLimit.
type FileTooLargeError struct {
	Path  string
	Size  int64
	Limit int64
}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("fshash: file %q is %d bytes, exceeds limit %d", e.Path, e.Size, e.Limit)
}
