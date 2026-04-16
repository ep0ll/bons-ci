package fshash

import (
	"context"
	"fmt"
	"sort"
)

// ── ChangeStatus ──────────────────────────────────────────────────────────────

// ChangeStatus classifies a path in a TreeComparison.
type ChangeStatus uint8

const (
	StatusUnchanged ChangeStatus = iota
	StatusAdded
	StatusRemoved
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
	RelPath string
	Status  ChangeStatus
	Kind    EntryKind // kind in the tree where the entry is present
	DigestA []byte    // zero for StatusAdded
	DigestB []byte    // zero for StatusRemoved
}

func (c TreeChange) String() string {
	return fmt.Sprintf("%s %s %s", c.Status, c.Kind, c.RelPath)
}

// ── TreeComparison ────────────────────────────────────────────────────────────

// TreeComparison is the full per-entry result of CompareTrees.
type TreeComparison struct {
	Changes []TreeChange // sorted by RelPath
	RootA   []byte
	RootB   []byte
}

// Equal reports whether both trees have identical root digests.
func (tc *TreeComparison) Equal() bool { return digestsEqual(tc.RootA, tc.RootB) }

// OnlyChanged filters out StatusUnchanged entries.
func (tc *TreeComparison) OnlyChanged() []TreeChange {
	out := make([]TreeChange, 0, len(tc.Changes))
	for _, c := range tc.Changes {
		if c.Status != StatusUnchanged {
			out = append(out, c)
		}
	}
	return out
}

// CountByStatus returns a map of status → count including zero values.
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

// Summary returns a one-line human-readable description.
func (tc *TreeComparison) Summary() string {
	if tc.Equal() {
		return fmt.Sprintf("identical (%d entries)", len(tc.Changes))
	}
	counts := tc.CountByStatus()
	return fmt.Sprintf("added=%d removed=%d modified=%d unchanged=%d",
		counts[StatusAdded], counts[StatusRemoved],
		counts[StatusModified], counts[StatusUnchanged])
}

// ── CompareTrees ──────────────────────────────────────────────────────────────

// CompareTrees performs a full per-entry comparison between two directory trees.
// Both Sum calls run concurrently. Every unique path in either tree has exactly
// one TreeChange in the result, including unchanged entries.
func (cs *Checksummer) CompareTrees(ctx context.Context, absPathA, absPathB string) (*TreeComparison, error) {
	type sr struct {
		res Result
		err error
	}
	chA, chB := make(chan sr, 1), make(chan sr, 1)
	go func() { r, e := cs.withCollect().Sum(ctx, absPathA); chA <- sr{r, e} }()
	go func() { r, e := cs.withCollect().Sum(ctx, absPathB); chB <- sr{r, e} }()

	rA, rB := <-chA, <-chB
	if rA.err != nil {
		return nil, fmt.Errorf("fshash: CompareTrees A: %w", rA.err)
	}
	if rB.err != nil {
		return nil, fmt.Errorf("fshash: CompareTrees B: %w", rB.err)
	}

	type rec struct {
		kind   EntryKind
		digest []byte
	}
	mapA := make(map[string]rec, len(rA.res.Entries))
	for _, e := range rA.res.Entries {
		if e.RelPath != "." {
			mapA[e.RelPath] = rec{e.Kind, e.Digest}
		}
	}
	mapB := make(map[string]rec, len(rB.res.Entries))
	for _, e := range rB.res.Entries {
		if e.RelPath != "." {
			mapB[e.RelPath] = rec{e.Kind, e.Digest}
		}
	}

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
		ch := TreeChange{RelPath: p}
		switch {
		case inA && !inB:
			ch.Status, ch.Kind, ch.DigestA = StatusRemoved, ea.kind, ea.digest
		case !inA && inB:
			ch.Status, ch.Kind, ch.DigestB = StatusAdded, eb.kind, eb.digest
		default:
			ch.Kind, ch.DigestA, ch.DigestB = eb.kind, ea.digest, eb.digest
			if digestsEqual(ea.digest, eb.digest) {
				ch.Status = StatusUnchanged
			} else {
				ch.Status = StatusModified
			}
		}
		changes = append(changes, ch)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].RelPath < changes[j].RelPath })

	return &TreeComparison{Changes: changes, RootA: rA.res.Digest, RootB: rB.res.Digest}, nil
}
