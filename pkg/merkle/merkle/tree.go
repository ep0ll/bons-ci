package merkle

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// MerkleTree
// ─────────────────────────────────────────────────────────────────────────────

// MerkleTree is an incremental, sealable Merkle tree for one layer's
// accessed files. Leaves are deduplicated by FilePath: if AddLeaf is called
// multiple times for the same path, only the last hash wins (ensuring that
// a mutating write after a read records the final content).
//
// The tree is NOT sealed until Seal() is called. Before sealing:
//   - AddLeaf, RecordDeletion, and Len are available.
//   - Root, Proof, and Leaves return errors.
//
// After sealing:
//   - The tree is immutable; AddLeaf and RecordDeletion are no-ops.
//   - Root, Proof, and Leaves return their computed values.
//
// Thread safety: all exported methods acquire the internal mutex and are safe
// for concurrent use.
type MerkleTree struct {
	mu        sync.RWMutex
	leafMap   map[string]Leaf // filePath → Leaf (pre-seal accumulator)
	deletions map[string]struct{}

	// sealed state — populated by Seal()
	sealed       bool
	sortedLeaves []Leaf   // leaves in sorted order
	leafHashes   [][]byte // leafHashes[i] = sortedLeaves[i].LeafHash()
	levels       [][][]byte
	root         []byte
}

// newMerkleTree allocates an unsealed MerkleTree.
func newMerkleTree() *MerkleTree {
	return &MerkleTree{
		leafMap:   make(map[string]Leaf),
		deletions: make(map[string]struct{}),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mutation (pre-seal)
// ─────────────────────────────────────────────────────────────────────────────

// AddLeaf adds or updates the leaf for filePath. Returns true if this is the
// first time filePath was added; false if it was updated. After sealing,
// AddLeaf is a no-op and returns false.
func (t *MerkleTree) AddLeaf(filePath string, hash []byte, algorithm string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sealed {
		return false
	}
	_, existed := t.leafMap[filePath]
	t.leafMap[filePath] = Leaf{
		FilePath:  filePath,
		Hash:      cloneBytes(hash),
		Algorithm: algorithm,
	}
	// If this file was previously recorded as deleted, lift the deletion.
	delete(t.deletions, filePath)
	return !existed
}

// RecordDeletion records that filePath was deleted in this layer. If a leaf
// exists for this path, it is removed. After sealing, RecordDeletion is a no-op.
func (t *MerkleTree) RecordDeletion(filePath string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sealed {
		return
	}
	delete(t.leafMap, filePath)
	t.deletions[filePath] = struct{}{}
}

// Len returns the number of leaves accumulated so far.
func (t *MerkleTree) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.leafMap)
}

// ─────────────────────────────────────────────────────────────────────────────
// Seal
// ─────────────────────────────────────────────────────────────────────────────

// Seal finalises the tree and computes the Merkle root. After sealing the tree
// is immutable. Seal is idempotent: calling it again returns the same root.
//
// Edge cases:
//   - Zero leaves: root = sha256("") (the hash of an empty input).
//   - One leaf:    root = leafHash (no internal nodes).
func (t *MerkleTree) Seal() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sealed {
		return cloneBytes(t.root)
	}

	// Sort leaves by FilePath for determinism.
	leaves := make([]Leaf, 0, len(t.leafMap))
	for _, l := range t.leafMap {
		leaves = append(leaves, l)
	}
	sort.Slice(leaves, func(i, j int) bool {
		return leaves[i].FilePath < leaves[j].FilePath
	})
	t.sortedLeaves = leaves

	if len(leaves) == 0 {
		// Empty tree: root is sha256("").
		empty := sha256.Sum256(nil)
		t.root = empty[:]
		t.sealed = true
		return cloneBytes(t.root)
	}

	// Compute leaf hashes.
	leafHashes := make([][]byte, len(leaves))
	for i, l := range leaves {
		leafHashes[i] = l.LeafHash()
	}
	t.leafHashes = leafHashes

	// Build levels bottom-up.
	t.levels = buildLevels(leafHashes)
	t.root = t.levels[len(t.levels)-1][0]
	t.sealed = true
	return cloneBytes(t.root)
}

// ─────────────────────────────────────────────────────────────────────────────
// Query (post-seal)
// ─────────────────────────────────────────────────────────────────────────────

// Root returns the Merkle root hash. Returns an error if the tree is not yet
// sealed.
func (t *MerkleTree) Root() ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.sealed {
		return nil, fmt.Errorf("merkle: tree not sealed; call Seal() first")
	}
	return cloneBytes(t.root), nil
}

// Leaves returns the sorted leaf list. Returns an error if not sealed.
func (t *MerkleTree) Leaves() ([]Leaf, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.sealed {
		return nil, fmt.Errorf("merkle: tree not sealed; call Seal() first")
	}
	cp := make([]Leaf, len(t.sortedLeaves))
	copy(cp, t.sortedLeaves)
	return cp, nil
}

// Deletions returns the set of file paths that were deleted in this layer.
func (t *MerkleTree) Deletions() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]string, 0, len(t.deletions))
	for p := range t.deletions {
		result = append(result, p)
	}
	sort.Strings(result)
	return result
}

// Proof returns a Merkle inclusion proof for the given filePath. Returns an
// error if the tree is not sealed, or if the filePath is not a leaf.
func (t *MerkleTree) Proof(filePath string) (*Proof, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if !t.sealed {
		return nil, fmt.Errorf("merkle: tree not sealed; call Seal() first")
	}

	// Find leaf index via binary search (leaves are sorted).
	idx := sort.Search(len(t.sortedLeaves), func(i int) bool {
		return t.sortedLeaves[i].FilePath >= filePath
	})
	if idx >= len(t.sortedLeaves) || t.sortedLeaves[idx].FilePath != filePath {
		return nil, fmt.Errorf("merkle: file %q is not a leaf in this tree", filePath)
	}

	if len(t.sortedLeaves) == 1 {
		// Single-leaf tree: proof has no siblings.
		return &Proof{
			FilePath: filePath,
			LeafHash: cloneBytes(t.leafHashes[0]),
			Root:     cloneBytes(t.root),
			Siblings: nil,
		}, nil
	}

	// Build the proof by walking up the level array.
	proof := &Proof{
		FilePath: filePath,
		LeafHash: cloneBytes(t.leafHashes[idx]),
		Root:     cloneBytes(t.root),
	}

	pos := idx
	for lvl := 0; lvl < len(t.levels)-1; lvl++ {
		level := t.levels[lvl]
		var sibIdx int
		var isLeft bool

		if pos%2 == 0 {
			// Current node is left child; sibling is right.
			sibIdx = pos + 1
			isLeft = false
			if sibIdx >= len(level) {
				// Odd node: paired with itself; sibling == self.
				sibIdx = pos
			}
		} else {
			// Current node is right child; sibling is left.
			sibIdx = pos - 1
			isLeft = true
		}

		proof.Siblings = append(proof.Siblings, SiblingStep{
			Hash:   cloneBytes(level[sibIdx]),
			IsLeft: isLeft,
		})
		pos /= 2
	}

	return proof, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildLevels constructs the full binary tree level array from leaf hashes.
// levels[0] = leaf hashes, levels[n] = [root].
func buildLevels(leafHashes [][]byte) [][][]byte {
	var levels [][][]byte
	current := make([][]byte, len(leafHashes))
	copy(current, leafHashes)
	levels = append(levels, current)

	for len(current) > 1 {
		next := make([][]byte, 0, (len(current)+1)/2)
		for i := 0; i < len(current); i += 2 {
			left := current[i]
			var right []byte
			if i+1 < len(current) {
				right = current[i+1]
			} else {
				// Odd node: pair with itself (balanced tree convention).
				right = current[i]
			}
			next = append(next, internalNodeHash(left, right))
		}
		levels = append(levels, next)
		current = next
	}
	return levels
}

// internalNodeHash computes sha256(left || right).
func internalNodeHash(left, right []byte) []byte {
	h := sha256.New()
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// sha256Bytes computes sha256(data) and returns the raw bytes.
func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// cloneBytes returns a fresh copy of b, or nil if b is nil.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
