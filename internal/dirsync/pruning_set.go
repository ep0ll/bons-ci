package dirsync

import (
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// PruningSet
// ─────────────────────────────────────────────────────────────────────────────

// PruningSet is a concurrency-safe, prefix-aware set of [ExclusivePath] entries.
//
// # Purpose
//
// It models the minimum collection of filesystem paths needed to atomically
// remove all exclusive-lower entries from a merged directory view, using the
// fewest possible syscalls.
//
// # Key invariant — collapsed directory subsumption
//
//   - A collapsed directory entry subsumes all its descendants.
//     Adding a descendant of an already-collapsed ancestor is silently ignored.
//   - Adding a new collapsed directory prunes any descendants already in the
//     set, because the new entry's os.RemoveAll will cover them all.
//
// This guarantees that one os.RemoveAll (or one io_uring unlinkat) per entry
// removes the entire exclusive-lower subtree with the minimum syscalls.
//
// # When to use PruningSet vs. direct streaming
//
// The [DirsyncClassifier] already emits collapsed entries and never emits
// children of collapsed directories, so for straight-through streaming the
// PruningSet is not strictly necessary.
//
// PruningSet becomes valuable when:
//   - Entries arrive from multiple goroutines or multiple classifiers.
//   - Entries are accumulated from a channel and batch-processed later.
//   - Callers need to query coverage before performing downstream operations.
//
// # Example
//
//	var ps dirsync.PruningSet
//	exclusive, _, errs := classifier.Classify(ctx)
//	for ep := range exclusive {
//	    ps.Add(ep)
//	}
//	ps.Drain(func(ep dirsync.ExclusivePath) {
//	    os.RemoveAll(filepath.Join(mergedRoot, ep.Path))
//	})
type PruningSet struct {
	mu      sync.Mutex
	entries []ExclusivePath // non-overlapping; collapsed dirs subsume descendants
}

// Add inserts ep into the set while maintaining the collapse invariant.
//
// Returns true when ep was accepted (and possibly caused existing entries to
// be pruned). Returns false when ep is already subsumed by an existing
// collapsed ancestor — in that case the set is unchanged.
func (ps *PruningSet) Add(ep ExclusivePath) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Reject ep if an existing collapsed ancestor already covers it.
	for i := range ps.entries {
		if ps.entries[i].Collapsed && isPathUnder(ep.Path, ps.entries[i].Path) {
			return false
		}
	}

	if ep.Collapsed {
		// The new collapsed directory subsumes any descendants already in the set.
		// Compact in-place to avoid a separate allocation.
		n := 0
		for _, existing := range ps.entries {
			if !isPathUnder(existing.Path, ep.Path) {
				ps.entries[n] = existing
				n++
			}
		}
		ps.entries = append(ps.entries[:n], ep)
	} else {
		ps.entries = append(ps.entries, ep)
	}
	return true
}

// Len returns the current number of set members.
func (ps *PruningSet) Len() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.entries)
}

// Covered reports whether path is already subsumed by a collapsed entry in the
// set. This is O(n) in the number of entries; for hot paths prefer [Add] which
// performs the same check as a side-effect of insertion.
func (ps *PruningSet) Covered(path string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for i := range ps.entries {
		if ps.entries[i].Collapsed && isPathUnder(path, ps.entries[i].Path) {
			return true
		}
	}
	return false
}

// Entries returns a point-in-time snapshot of the set.
// The returned slice is a copy; modifications do not affect the set.
func (ps *PruningSet) Entries() []ExclusivePath {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]ExclusivePath, len(ps.entries))
	copy(out, ps.entries)
	return out
}

// ForEach calls fn for each entry in insertion order, without removing entries.
//
// A point-in-time snapshot is taken before iteration begins so the set may be
// safely modified from fn or from concurrent goroutines during the call.
// Unlike [Drain], ForEach leaves the set intact.
func (ps *PruningSet) ForEach(fn func(ExclusivePath)) {
	for _, ep := range ps.Entries() {
		fn(ep)
	}
}

// Drain atomically removes all entries from the set and calls fn for each one.
// fn is invoked outside the lock, so the set may be safely modified from fn
// or from concurrent goroutines during iteration.
//
// This is the preferred pattern for batch processing because it avoids holding
// the lock across potentially expensive I/O operations such as os.RemoveAll.
func (ps *PruningSet) Drain(fn func(ExclusivePath)) {
	ps.mu.Lock()
	snapshot := ps.entries
	ps.entries = nil
	ps.mu.Unlock()

	for _, ep := range snapshot {
		fn(ep)
	}
}

// Reset clears the set without invoking any callback.
func (ps *PruningSet) Reset() {
	ps.mu.Lock()
	ps.entries = ps.entries[:0]
	ps.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Path hierarchy helper
// ─────────────────────────────────────────────────────────────────────────────

// isPathUnder reports whether child is equal to parent or is a direct or
// indirect descendant of parent in the filesystem path hierarchy.
//
// The path separator is always "/" because all paths in this package use
// forward slashes regardless of the host OS.
//
// Examples:
//
//	isPathUnder("a/b/c", "a/b")  → true   (grandchild)
//	isPathUnder("a/b",   "a/b")  → true   (same path)
//	isPathUnder("a/bc",  "a/b")  → false  (different component; not a child)
//	isPathUnder("a",     "a/b")  → false  (parent cannot be under child)
func isPathUnder(child, parent string) bool {
	return child == parent || strings.HasPrefix(child, parent+"/")
}
