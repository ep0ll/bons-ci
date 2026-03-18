package differ

import (
	"strings"
	"sync"
)

// PruningSet is a concurrency-safe, prefix-aware set of [ExclusivePath] entries.
//
// # Purpose
//
// It models the minimum collection of filesystem paths required to atomically
// remove all exclusive-lower entries from a merged directory view. The key
// invariant is:
//
//   - A collapsed directory entry subsumes all its descendants: adding any
//     descendant of an already-collapsed ancestor is a no-op.
//   - Adding a new collapsed directory prunes any descendants already in the
//     set, as they would be redundantly removed by the collapsed entry.
//
// This guarantees that iterating the set and invoking one os.RemoveAll (or one
// io_uring unlinkat) per entry deletes the entire exclusive-lower subtree with
// the minimum number of syscalls — one per top-level exclusive unit.
//
// # Usage with a Differ stream
//
// The [DirsyncDiffer] already emits collapsed entries and never emits children
// of collapsed directories, so for straight-through streaming the PruningSet
// is not strictly necessary. It becomes valuable when:
//
//   - Entries arrive from multiple goroutines or multiple differ instances.
//   - Entries are accumulated from a channel and later batch-processed.
//   - Callers need to query coverage before performing downstream operations.
//
// Example:
//
//	exclusive, _, errs := differ.Diff(ctx)
//	var ps differ.PruningSet
//	for ep := range exclusive {
//	    ps.Add(ep)
//	}
//	ps.Drain(func(ep differ.ExclusivePath) {
//	    os.RemoveAll(filepath.Join(mergedRoot, ep.Path))
//	})
type PruningSet struct {
	mu      sync.Mutex
	entries []ExclusivePath // non-overlapping; collapsed dirs subsume descendants
}

// Add inserts e into the set while maintaining the collapsing invariant.
//
// Returns true if e was accepted (and possibly caused existing entries to be
// pruned). Returns false if e is already subsumed by an existing collapsed
// ancestor — in that case the set is unchanged.
func (ps *PruningSet) Add(e ExclusivePath) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// If any collapsed ancestor already covers e, reject it.
	for i := range ps.entries {
		if ps.entries[i].Collapsed && pathIsUnder(e.Path, ps.entries[i].Path) {
			return false
		}
	}

	if e.Collapsed {
		// Prune any existing entries that are now subsumed by the new
		// collapsed dir. We compact in-place to avoid allocation.
		n := 0
		for _, r := range ps.entries {
			if !pathIsUnder(r.Path, e.Path) {
				ps.entries[n] = r
				n++
			}
		}
		ps.entries = append(ps.entries[:n], e)
	} else {
		ps.entries = append(ps.entries, e)
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
// performs the same check as a side-effect.
func (ps *PruningSet) Covered(path string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for i := range ps.entries {
		if ps.entries[i].Collapsed && pathIsUnder(path, ps.entries[i].Path) {
			return true
		}
	}
	return false
}

// Entries returns a point-in-time snapshot of the set. The returned slice is a
// copy; modifications do not affect the set.
func (ps *PruningSet) Entries() []ExclusivePath {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]ExclusivePath, len(ps.entries))
	copy(out, ps.entries)
	return out
}

// ForEach calls fn for each entry in insertion order.
// A point-in-time snapshot is taken before iteration begins so the set may be
// safely modified from fn or from concurrent goroutines during the call.
// Unlike [Drain], ForEach does not remove entries from the set.
func (ps *PruningSet) ForEach(fn func(ExclusivePath)) {
	for _, e := range ps.Entries() {
		fn(e)
	}
}

// Drain atomically removes all entries from the set (resetting it to empty)
// and calls fn for each one. fn is invoked outside the lock, so the set may
// be safely modified from fn or from other goroutines during iteration.
//
// This is the preferred pattern for batch processing because it avoids holding
// the lock across potentially expensive I/O operations such as os.RemoveAll.
func (ps *PruningSet) Drain(fn func(ExclusivePath)) {
	ps.mu.Lock()
	entries := ps.entries
	ps.entries = nil
	ps.mu.Unlock()

	for _, e := range entries {
		fn(e)
	}
}

// Reset clears the set without invoking any callback.
func (ps *PruningSet) Reset() {
	ps.mu.Lock()
	ps.entries = ps.entries[:0]
	ps.mu.Unlock()
}

// pathIsUnder returns true when child is equal to parent or is a direct or
// indirect descendant of parent in the filesystem path hierarchy.
//
// Example:
//
//	pathIsUnder("a/b/c", "a/b")  → true
//	pathIsUnder("a/b",   "a/b")  → true
//	pathIsUnder("a/bc",  "a/b")  → false  (different path component)
func pathIsUnder(child, parent string) bool {
	return child == parent || strings.HasPrefix(child, parent+"/")
}
