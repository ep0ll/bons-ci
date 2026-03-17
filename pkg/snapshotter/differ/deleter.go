package diffview

// deleter.go – the Deleter interface and its concrete implementations.
//
// A Deleter performs a single deletion for one DiffEntry whose Action is
// ActionDelete.  The interface is intentionally minimal: one method, one
// argument, one return — making it trivial to implement alternative backends
// (cloud storage, audit-log-only, mock for tests, etc.).

import (
	"fmt"
	"os"
	"sync"
)

// ─── Deleter interface ────────────────────────────────────────────────────────

// Deleter removes a single entry from the merged directory.
//
// Implementations must be safe to call concurrently from multiple goroutines.
// Delete receives only entries where Action == ActionDelete; the Apply engine
// enforces this invariant.
type Deleter interface {
	// Delete removes the entry.  A nil return means the path was successfully
	// removed (or, for DryRunDeleter, would be removed).
	Delete(entry DiffEntry) error
}

// ─── FSDeleter ────────────────────────────────────────────────────────────────

// FSDeleter performs real filesystem deletions using os.RemoveAll / os.Remove.
//
// Deletion strategy (leveraging the dirsync pruning DSA):
//
//   Pruned == true (exclusive dir root):
//     os.RemoveAll(MergedAbs) — one syscall removes the entire subtree
//     regardless of depth.  This is the O(1)-per-root payoff.
//
//   IsDir == true (non-pruned directory, e.g. a common empty dir):
//     os.RemoveAll — safe for empty dirs; handles unexpected non-empty cases.
//
//   Regular file or symlink:
//     os.Remove — cheaper than RemoveAll for leaf nodes.
//
// FSDeleter is safe for concurrent use (all methods are stateless).
type FSDeleter struct{}

// NewFSDeleter creates a new FSDeleter.
func NewFSDeleter() *FSDeleter { return &FSDeleter{} }

// Delete implements Deleter.
func (d *FSDeleter) Delete(entry DiffEntry) error {
	var err error
	if entry.Pruned || entry.IsDir {
		err = os.RemoveAll(entry.MergedAbs)
	} else {
		err = os.Remove(entry.MergedAbs)
	}
	if err != nil {
		return fmt.Errorf("delete %q: %w", entry.MergedAbs, err)
	}
	return nil
}

// ─── DryRunDeleter ────────────────────────────────────────────────────────────

// DryRunDeleter is a Deleter that records every would-be deletion without
// touching the filesystem.  It is useful for previewing the effect of Apply
// before committing, or for unit tests.
//
// Recorded entries are available via Entries() after Apply returns.
// DryRunDeleter is safe for concurrent use.
type DryRunDeleter struct {
	mu      sync.Mutex
	entries []DiffEntry
}

// NewDryRunDeleter creates a new DryRunDeleter.
func NewDryRunDeleter() *DryRunDeleter { return &DryRunDeleter{} }

// Delete implements Deleter — records the entry without touching the filesystem.
func (d *DryRunDeleter) Delete(entry DiffEntry) error {
	d.mu.Lock()
	d.entries = append(d.entries, entry)
	d.mu.Unlock()
	return nil
}

// Entries returns a snapshot of all entries that would have been deleted,
// in the order they were received.  Safe to call after Apply returns.
func (d *DryRunDeleter) Entries() []DiffEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DiffEntry, len(d.entries))
	copy(out, d.entries)
	return out
}
