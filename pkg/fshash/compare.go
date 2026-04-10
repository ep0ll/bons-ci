package fshash

import (
	"context"
	"fmt"
	"sort"
)

// ── ChangeStatus ──────────────────────────────────────────────────────────────

// ChangeStatus classifies a path in a [TreeComparison].
type ChangeStatus uint8

const (
	// StatusUnchanged means the path exists in both trees with identical digests.
	StatusUnchanged ChangeStatus = iota
	// StatusAdded means the path exists only in the second tree (B).
	StatusAdded
	// StatusRemoved means the path exists only in the first tree (A).
	StatusRemoved
	// StatusModified means the path exists in both trees but has different digests.
	StatusModified
)

func (s ChangeStatus) String() string {
	switch s {
	case StatusUnchanged:
		return "unchanged"
	case StatusAdded:
		return "added"
	case StatusRemoved:
		return "removed"
	case StatusModified:
		return "modified"
	default:
		return "unknown"
	}
}

// ── TreeChange ────────────────────────────────────────────────────────────────

// TreeChange describes the state of a single path across two trees.
type TreeChange struct {
	// RelPath is the slash-separated path relative to the tree root.
	RelPath string
	// Status describes what changed (or didn't).
	Status ChangeStatus
	// Kind is the entry kind in the tree where the entry is present.
	// For StatusModified, it reflects the kind in tree B.
	Kind EntryKind
	// DigestA is the digest in tree A (zero for StatusAdded).
	DigestA []byte
	// DigestB is the digest in tree B (zero for StatusRemoved).
	DigestB []byte
}

func (c TreeChange) String() string {
	return fmt.Sprintf("%s %s %s", c.Status, c.Kind, c.RelPath)
}

// ── TreeComparison ────────────────────────────────────────────────────────────

// TreeComparison is the full per-entry result of [Checksummer.CompareTrees].
type TreeComparison struct {
	// Changes holds one record per unique path, sorted by RelPath.
	Changes []TreeChange
	// RootA is the root digest of tree A.
	RootA []byte
	// RootB is the root digest of tree B.
	RootB []byte
}

// Equal returns true if both trees have identical root digests.
func (tc *TreeComparison) Equal() bool {
	if len(tc.RootA) != len(tc.RootB) {
		return false
	}
	for i := range tc.RootA {
		if tc.RootA[i] != tc.RootB[i] {
			return false
		}
	}
	return true
}

// OnlyChanged returns a slice of changes that are not StatusUnchanged.
func (tc *TreeComparison) OnlyChanged() []TreeChange {
	out := make([]TreeChange, 0, len(tc.Changes))
	for _, c := range tc.Changes {
		if c.Status != StatusUnchanged {
			out = append(out, c)
		}
	}
	return out
}

// CountByStatus returns the number of changes with each status.
func (tc *TreeComparison) CountByStatus() map[ChangeStatus]int {
	m := map[ChangeStatus]int{
		StatusUnchanged: 0,
		StatusAdded:     0,
		StatusRemoved:   0,
		StatusModified:  0,
	}
	for _, c := range tc.Changes {
		m[c.Status]++
	}
	return m
}

// Summary returns a one-line human-readable summary.
func (tc *TreeComparison) Summary() string {
	counts := tc.CountByStatus()
	if tc.Equal() {
		return fmt.Sprintf("identical (%d entries)", len(tc.Changes))
	}
	return fmt.Sprintf("added=%d removed=%d modified=%d unchanged=%d",
		counts[StatusAdded], counts[StatusRemoved],
		counts[StatusModified], counts[StatusUnchanged])
}

// ── CompareTrees ──────────────────────────────────────────────────────────────

// CompareTrees performs a full per-entry comparison between two directory trees
// rooted at absPathA and absPathB.  It returns a [TreeComparison] containing
// one [TreeChange] per unique path found in either tree, including unchanged
// entries.
//
// CompareTrees runs both Sum calls concurrently (like [Checksummer.ParallelDiff])
// and is the richest comparison API in the package.  Use [Checksummer.Diff] or
// [Checksummer.ParallelDiff] when you only need added/removed/modified sets.
func (cs *Checksummer) CompareTrees(ctx context.Context, absPathA, absPathB string) (*TreeComparison, error) {
	type sumResult struct {
		res Result
		err error
	}

	chA := make(chan sumResult, 1)
	chB := make(chan sumResult, 1)

	go func() {
		res, err := cs.withCollect().Sum(ctx, absPathA)
		chA <- sumResult{res, err}
	}()
	go func() {
		res, err := cs.withCollect().Sum(ctx, absPathB)
		chB <- sumResult{res, err}
	}()

	rA, rB := <-chA, <-chB
	if rA.err != nil {
		return nil, fmt.Errorf("fshash: CompareTrees A: %w", rA.err)
	}
	if rB.err != nil {
		return nil, fmt.Errorf("fshash: CompareTrees B: %w", rB.err)
	}

	// Index entries by relPath.
	type entryRecord struct {
		kind   EntryKind
		digest []byte
	}
	mapA := make(map[string]entryRecord, len(rA.res.Entries))
	for _, e := range rA.res.Entries {
		if e.RelPath == "." {
			continue // root represented by RootA/RootB digest fields
		}
		mapA[e.RelPath] = entryRecord{kind: e.Kind, digest: e.Digest}
	}
	mapB := make(map[string]entryRecord, len(rB.res.Entries))
	for _, e := range rB.res.Entries {
		if e.RelPath == "." {
			continue
		}
		mapB[e.RelPath] = entryRecord{kind: e.Kind, digest: e.Digest}
	}

	// Collect all unique paths.
	seen := make(map[string]struct{}, len(mapA)+len(mapB))
	for p := range mapA {
		seen[p] = struct{}{}
	}
	for p := range mapB {
		seen[p] = struct{}{}
	}

	changes := make([]TreeChange, 0, len(seen))
	for p := range seen {
		ea, inA := mapA[p]
		eb, inB := mapB[p]

		var ch TreeChange
		ch.RelPath = p

		switch {
		case inA && !inB:
			ch.Status = StatusRemoved
			ch.Kind = ea.kind
			ch.DigestA = ea.digest
		case !inA && inB:
			ch.Status = StatusAdded
			ch.Kind = eb.kind
			ch.DigestB = eb.digest
		default: // both present
			ch.Kind = eb.kind
			ch.DigestA = ea.digest
			ch.DigestB = eb.digest
			if equal(ea.digest, eb.digest) {
				ch.Status = StatusUnchanged
			} else {
				ch.Status = StatusModified
			}
		}
		changes = append(changes, ch)
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].RelPath < changes[j].RelPath
	})

	return &TreeComparison{
		Changes: changes,
		RootA:   rA.res.Digest,
		RootB:   rB.res.Digest,
	}, nil
}

// equal compares two byte slices without importing bytes (avoids a new import
// in this file; the comparison is trivial).
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
