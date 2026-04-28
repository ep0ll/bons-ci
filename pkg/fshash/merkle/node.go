package merkle

import "github.com/bons/bons-ci/pkg/fshash/internal/core"

// NodeKind classifies a Merkle tree node.
type NodeKind uint8

const (
	NodeLeaf   NodeKind = iota // File content hash
	NodeBranch                 // Interior node from children
)

// String returns a human-readable node kind.
func (k NodeKind) String() string {
	switch k {
	case NodeLeaf:
		return "leaf"
	case NodeBranch:
		return "branch"
	default:
		return "unknown"
	}
}

// Node is an element of the Merkle tree.
type Node struct {
	Kind     NodeKind
	Hash     []byte
	Path     string     // Non-empty for leaves
	Children []*Node    // Non-nil for branches
	Layer    core.LayerID // Source layer for provenance
	Depth    int
}

// IsLeaf reports whether this is a leaf node.
func (n *Node) IsLeaf() bool { return n.Kind == NodeLeaf }

// IsBranch reports whether this is a branch node.
func (n *Node) IsBranch() bool { return n.Kind == NodeBranch }

// LeafCount returns total leaf count in this subtree.
func (n *Node) LeafCount() int {
	if n.IsLeaf() {
		return 1
	}
	count := 0
	for _, child := range n.Children {
		count += child.LeafCount()
	}
	return count
}

// Walk visits every node depth-first pre-order.
func (n *Node) Walk(fn func(node *Node, depth int) bool) {
	n.walk(fn, 0)
}

func (n *Node) walk(fn func(*Node, int) bool, depth int) bool {
	if !fn(n, depth) {
		return false
	}
	for _, child := range n.Children {
		if !child.walk(fn, depth+1) {
			return false
		}
	}
	return true
}
