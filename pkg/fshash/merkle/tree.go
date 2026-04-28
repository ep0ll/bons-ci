package merkle

import (
	"fmt"
	"sort"

	"github.com/bons/bons-ci/pkg/fshash/chunk"
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// TreeOption configures the Merkle tree builder.
type TreeOption func(*treeConfig)

type treeConfig struct {
	branchingFactor int
	algorithm       chunk.Algorithm
}

// WithBranchingFactor sets the branching factor. Default: 2 (binary tree).
func WithBranchingFactor(n int) TreeOption {
	return func(c *treeConfig) {
		if n >= 2 {
			c.branchingFactor = n
		}
	}
}

// WithTreeAlgorithm sets the hash algorithm for branch nodes.
func WithTreeAlgorithm(algo chunk.Algorithm) TreeOption {
	return func(c *treeConfig) {
		c.algorithm = algo
	}
}

// Tree is an incremental Merkle tree builder. Leaves are sorted by path
// for deterministic root hashes, and pre-computed subtrees can be inserted
// directly for cross-layer reuse.
type Tree struct {
	cfg    treeConfig
	hasher chunk.Hasher
	leaves []*Node
	root   *Node
	built  bool
}

// NewTree creates a Merkle tree builder.
func NewTree(opts ...TreeOption) *Tree {
	cfg := treeConfig{
		branchingFactor: 2,
		algorithm:       chunk.BLAKE3,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Tree{
		cfg:    cfg,
		hasher: chunk.NewHasher(cfg.algorithm),
	}
}

// Insert adds a file as a leaf node.
func (t *Tree) Insert(path string, hash []byte, layerID core.LayerID) {
	t.leaves = append(t.leaves, &Node{
		Kind:  NodeLeaf,
		Hash:  copyBytes(hash),
		Path:  path,
		Layer: layerID,
	})
	t.built = false
}

// InsertSubtree adds a pre-computed subtree for cross-layer reuse.
func (t *Tree) InsertSubtree(node *Node) {
	t.leaves = append(t.leaves, node)
	t.built = false
}

// Build constructs the tree bottom-up. Leaves are sorted by path.
func (t *Tree) Build() (*Node, error) {
	if len(t.leaves) == 0 {
		return nil, core.ErrTreeEmpty
	}

	sort.Slice(t.leaves, func(i, j int) bool {
		return t.leaves[i].Path < t.leaves[j].Path
	})

	currentLevel := t.leaves
	depth := 0

	for len(currentLevel) > 1 {
		depth++
		var nextLevel []*Node
		for i := 0; i < len(currentLevel); i += t.cfg.branchingFactor {
			end := i + t.cfg.branchingFactor
			if end > len(currentLevel) {
				end = len(currentLevel)
			}
			branch := t.combineNodes(currentLevel[i:end], depth)
			nextLevel = append(nextLevel, branch)
		}
		currentLevel = nextLevel
	}

	t.root = currentLevel[0]
	t.built = true
	return t.root, nil
}

// Root returns the Merkle root hash. Nil if not built.
func (t *Tree) Root() []byte {
	if t.root == nil {
		return nil
	}
	return copyBytes(t.root.Hash)
}

// RootNode returns the root node.
func (t *Tree) RootNode() *Node { return t.root }

// LeafCount returns the number of leaves inserted.
func (t *Tree) LeafCount() int { return len(t.leaves) }

func (t *Tree) combineNodes(children []*Node, depth int) *Node {
	combined := make([]byte, 0, len(children)*t.hasher.Size())
	for _, child := range children {
		combined = append(combined, child.Hash...)
	}
	childCopy := make([]*Node, len(children))
	copy(childCopy, children)
	return &Node{
		Kind:     NodeBranch,
		Hash:     t.hasher.Hash(combined),
		Children: childCopy,
		Depth:    depth,
	}
}

// Verify checks internal consistency of the tree.
func (t *Tree) Verify() error {
	if t.root == nil {
		return core.ErrTreeEmpty
	}
	return t.verifyNode(t.root)
}

func (t *Tree) verifyNode(n *Node) error {
	if n.IsLeaf() {
		return nil
	}
	combined := make([]byte, 0, len(n.Children)*t.hasher.Size())
	for _, child := range n.Children {
		combined = append(combined, child.Hash...)
	}
	expected := t.hasher.Hash(combined)
	if !bytesEqual(n.Hash, expected) {
		return fmt.Errorf("fshash: branch hash mismatch at depth %d", n.Depth)
	}
	for _, child := range n.Children {
		if err := t.verifyNode(child); err != nil {
			return err
		}
	}
	return nil
}

func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func bytesEqual(a, b []byte) bool {
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
