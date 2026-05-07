package layermerkle

import (
	"sort"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// ForestDiff — structural comparison of two MerkleForests
// ─────────────────────────────────────────────────────────────────────────────

// ForestDiff holds the result of comparing two MerkleForests.
// It exposes the set of vertices that are new, removed, changed, or identical.
type ForestDiff struct {
	// Added contains vertices present in B but not A.
	Added []*MerkleTree

	// Removed contains vertices present in A but not B.
	Removed []*MerkleTree

	// Changed contains vertices present in both A and B whose Merkle root differs.
	Changed []TreeChange

	// Unchanged contains vertices present in both A and B with identical roots.
	Unchanged []*MerkleTree
}

// TreeChange describes a vertex whose Merkle root changed between two forests.
type TreeChange struct {
	// VertexID is the ExecOp that changed.
	VertexID VertexID

	// Before is the tree from forest A.
	Before *MerkleTree

	// After is the tree from forest B.
	After *MerkleTree

	// FileDiff is the per-file diff within this vertex (computed lazily).
	FileDiff *FileDiff
}

// IsIdentical reports whether both forests contain the same vertex IDs with
// identical Merkle roots.
func (d *ForestDiff) IsIdentical() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// Summary returns a human-readable summary of the diff.
func (d *ForestDiff) Summary() string {
	var sb strings.Builder
	sb.WriteString("ForestDiff{")
	sb.WriteString("added=")
	sb.WriteString(itoa(len(d.Added)))
	sb.WriteString(" removed=")
	sb.WriteString(itoa(len(d.Removed)))
	sb.WriteString(" changed=")
	sb.WriteString(itoa(len(d.Changed)))
	sb.WriteString(" unchanged=")
	sb.WriteString(itoa(len(d.Unchanged)))
	sb.WriteString("}")
	return sb.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// FileDiff — per-file changes within one vertex
// ─────────────────────────────────────────────────────────────────────────────

// FileDiff holds the per-file access pattern difference between two trees.
type FileDiff struct {
	// Added contains files accessed in After but not Before.
	Added []FileChange
	// Removed contains files accessed in Before but not After.
	Removed []FileChange
	// Modified contains files whose hash changed between Before and After.
	Modified []FileChange
	// Unchanged contains files with identical hashes in both trees.
	Unchanged []FileChange
}

// FileChange describes one file's state in a diff.
type FileChange struct {
	RelPath      string
	BeforeHash   FileHash
	AfterHash    FileHash
	OwnerLayerID LayerID
}

// ─────────────────────────────────────────────────────────────────────────────
// Diff — compute ForestDiff between two forests
// ─────────────────────────────────────────────────────────────────────────────

// Diff computes the structural difference between forest A and forest B.
// File-level diffs within changed vertices are not computed by default;
// call TreeChange.ComputeFileDiff() to populate FileDiff on demand.
func Diff(a, b *MerkleForest) *ForestDiff {
	d := &ForestDiff{}

	aAll := indexTrees(a.All())
	bAll := indexTrees(b.All())

	for id, bTree := range bAll {
		if aTree, ok := aAll[id]; ok {
			if aTree.Root == bTree.Root {
				d.Unchanged = append(d.Unchanged, bTree)
			} else {
				d.Changed = append(d.Changed, TreeChange{
					VertexID: id,
					Before:   aTree,
					After:    bTree,
				})
			}
		} else {
			d.Added = append(d.Added, bTree)
		}
	}

	for id, aTree := range aAll {
		if _, ok := bAll[id]; !ok {
			d.Removed = append(d.Removed, aTree)
		}
	}

	sortTrees(d.Added)
	sortTrees(d.Removed)
	sortTrees(d.Unchanged)
	sortChanges(d.Changed)

	return d
}

// ComputeFileDiff populates tc.FileDiff by walking the leaves of both trees.
// This is O(n log n) in the number of leaves.
func (tc *TreeChange) ComputeFileDiff() *FileDiff {
	if tc.FileDiff != nil {
		return tc.FileDiff
	}
	tc.FileDiff = diffLeaves(tc.Before, tc.After)
	return tc.FileDiff
}

// ─────────────────────────────────────────────────────────────────────────────
// diffLeaves — O(n log n) leaf-level diff
// ─────────────────────────────────────────────────────────────────────────────

func diffLeaves(before, after *MerkleTree) *FileDiff {
	d := &FileDiff{}

	beforeIdx := indexLeaves(before.Leaves)
	afterIdx := indexLeaves(after.Leaves)

	for path, afterLeaf := range afterIdx {
		if beforeLeaf, ok := beforeIdx[path]; ok {
			if beforeLeaf.Hash == afterLeaf.Hash {
				d.Unchanged = append(d.Unchanged, FileChange{
					RelPath:      path,
					BeforeHash:   beforeLeaf.Hash,
					AfterHash:    afterLeaf.Hash,
					OwnerLayerID: afterLeaf.OwnerLayerID,
				})
			} else {
				d.Modified = append(d.Modified, FileChange{
					RelPath:      path,
					BeforeHash:   beforeLeaf.Hash,
					AfterHash:    afterLeaf.Hash,
					OwnerLayerID: afterLeaf.OwnerLayerID,
				})
			}
		} else {
			d.Added = append(d.Added, FileChange{
				RelPath:   path,
				AfterHash: afterLeaf.Hash,
			})
		}
	}

	for path, beforeLeaf := range beforeIdx {
		if _, ok := afterIdx[path]; !ok {
			d.Removed = append(d.Removed, FileChange{
				RelPath:    path,
				BeforeHash: beforeLeaf.Hash,
			})
		}
	}

	sortFileChanges(d.Added)
	sortFileChanges(d.Removed)
	sortFileChanges(d.Modified)
	sortFileChanges(d.Unchanged)

	return d
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func indexTrees(trees []*MerkleTree) map[VertexID]*MerkleTree {
	m := make(map[VertexID]*MerkleTree, len(trees))
	for _, t := range trees {
		m[t.VertexID] = t
	}
	return m
}

func indexLeaves(leaves []*MerkleLeaf) map[string]*MerkleLeaf {
	m := make(map[string]*MerkleLeaf, len(leaves))
	for _, l := range leaves {
		m[l.RelPath] = l
	}
	return m
}

func sortTrees(trees []*MerkleTree) {
	sort.Slice(trees, func(i, j int) bool {
		return trees[i].VertexID < trees[j].VertexID
	})
}

func sortChanges(changes []TreeChange) {
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].VertexID < changes[j].VertexID
	})
}

func sortFileChanges(fc []FileChange) {
	sort.Slice(fc, func(i, j int) bool {
		return fc[i].RelPath < fc[j].RelPath
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
