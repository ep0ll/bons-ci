package layermerkle

import (
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/hash"
	"github.com/bons/bons-ci/pkg/layermerkle/internal/trie"
	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// MerkleLeaf — a single file access record
// ─────────────────────────────────────────────────────────────────────────────

// MerkleLeaf is an immutable file access record that forms a leaf node in the
// Merkle tree. Its Hash is the SHA-256 of the file contents at access time.
type MerkleLeaf struct {
	// RelPath is the path relative to the overlay merged directory.
	RelPath string

	// Hash is the content-addressable digest of the file.
	Hash FileHash

	// OwnerLayerID identifies which layer's diff contains the file.
	OwnerLayerID LayerID

	// FromCache reports whether the hash was served from the dedup cache.
	FromCache bool
}

// ─────────────────────────────────────────────────────────────────────────────
// MerkleNode — an interior directory node in the Merkle tree
// ─────────────────────────────────────────────────────────────────────────────

// MerkleNode represents a directory in the Merkle tree. Its Hash is computed
// as sha256(sorted_children_hashes), creating a content-addressed subtree.
type MerkleNode struct {
	// Path is the directory path relative to the merged root.
	Path string

	// Hash is the sha256 of all direct children hashes sorted lexicographically.
	Hash []byte

	// Children are the direct child nodes (files or directories).
	Children []*MerkleNode

	// Leaf is non-nil for file leaf nodes.
	Leaf *MerkleLeaf
}

// IsLeaf reports whether this node is a file leaf.
func (n *MerkleNode) IsLeaf() bool { return n.Leaf != nil }

// HexHash returns the hex-encoded hash, or "" for unhashed nodes.
func (n *MerkleNode) HexHash() string { return hex.EncodeToString(n.Hash) }

// ─────────────────────────────────────────────────────────────────────────────
// MerkleTree — per-vertex Merkle tree of file accesses
// ─────────────────────────────────────────────────────────────────────────────

// MerkleTree is the output of the deduplication engine for one ExecOp vertex.
// Its Root is the content-addressable digest of all accessed files combined.
// Two identical access patterns always produce identical Root values.
type MerkleTree struct {
	// VertexID is the ExecOp that produced this tree.
	VertexID VertexID

	// LayerStack is the layer stack at the time of the ExecOp.
	LayerStack LayerStack

	// Root is the Merkle root digest.
	Root digest.Digest

	// RootNode is the root of the computed tree (for traversal).
	RootNode *MerkleNode

	// Leaves are all file leaf records in sorted order.
	Leaves []*MerkleLeaf

	// LeafCount is the total number of accessed files (including dedup hits).
	LeafCount int

	// CacheHitCount is the number of hashes served from the dedup cache.
	CacheHitCount int

	// FinalizedAt is when this tree was sealed.
	FinalizedAt time.Time
}

// CacheHitRate returns the fraction of file hashes served from cache.
func (t *MerkleTree) CacheHitRate() float64 {
	if t.LeafCount == 0 {
		return 0
	}
	return float64(t.CacheHitCount) / float64(t.LeafCount)
}

// String implements fmt.Stringer.
func (t *MerkleTree) String() string {
	return fmt.Sprintf("MerkleTree{vertex=%s root=%s leaves=%d hits=%d}",
		shortDigest(t.VertexID), t.Root, t.LeafCount, t.CacheHitCount)
}

// ─────────────────────────────────────────────────────────────────────────────
// MerkleTreeBuilder — assembles a MerkleTree from HashResults
// ─────────────────────────────────────────────────────────────────────────────

// MerkleTreeBuilder accumulates HashResults for one vertex and produces the
// final MerkleTree on Finalize(). It uses the internal trie package to
// efficiently compute intermediate directory hashes without string re-parsing.
//
// Not safe for concurrent use; the engine serializes calls per vertex.
type MerkleTreeBuilder struct {
	vertexID   VertexID
	layerStack LayerStack
	root       *trie.Node
	leaves     []*MerkleLeaf
	hitCount   int
	mu         sync.Mutex
}

// NewMerkleTreeBuilder returns a builder for the given vertex.
func NewMerkleTreeBuilder(vertexID VertexID, stack LayerStack) *MerkleTreeBuilder {
	return &MerkleTreeBuilder{
		vertexID:   vertexID,
		layerStack: stack,
		root:       trie.NewRoot(),
	}
}

// Add inserts a HashResult as a leaf. Safe for concurrent use.
func (b *MerkleTreeBuilder) Add(r *HashResult) {
	if r.Err != nil || r.Hash == "" {
		return
	}
	leaf := &MerkleLeaf{
		RelPath:      r.RelPath,
		Hash:         r.Hash,
		OwnerLayerID: r.OwnerLayerID,
		FromCache:    r.FromCache,
	}
	b.mu.Lock()
	b.root.Insert(r.RelPath, []byte(r.Hash))
	b.leaves = append(b.leaves, leaf)
	if r.FromCache {
		b.hitCount++
	}
	b.mu.Unlock()
}

// Finalize seals the builder and returns the complete MerkleTree.
// After Finalize, Add must not be called.
func (b *MerkleTreeBuilder) Finalize() *MerkleTree {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Sort leaves by RelPath for deterministic output.
	sort.Slice(b.leaves, func(i, j int) bool {
		return b.leaves[i].RelPath < b.leaves[j].RelPath
	})

	rootNode := buildMerkleNode("", b.root, b.leaves)
	root := computeRootDigest(rootNode)

	return &MerkleTree{
		VertexID:      b.vertexID,
		LayerStack:    b.layerStack,
		Root:          root,
		RootNode:      rootNode,
		Leaves:        b.leaves,
		LeafCount:     len(b.leaves),
		CacheHitCount: b.hitCount,
		FinalizedAt:   time.Now(),
	}
}

// buildMerkleNode recursively constructs the MerkleNode tree from the trie.
func buildMerkleNode(path string, n *trie.Node, _ []*MerkleLeaf) *MerkleNode {
	node := &MerkleNode{Path: path}
	for _, child := range n.SortedChildren() {
		var childPath string
		if path == "" {
			childPath = child.Segment
		} else {
			childPath = path + "/" + child.Segment
		}
		childNode := &MerkleNode{Path: childPath}
		if !child.IsDir {
			childNode.Leaf = &MerkleLeaf{RelPath: childPath}
			childNode.Hash = child.Hash
		} else {
			inner := buildMerkleNode(childPath, child, nil)
			childNode.Children = inner.Children
			childNode.Hash = inner.Hash
		}
		node.Children = append(node.Children, childNode)
	}
	node.Hash = hashChildren(node.Children)
	return node
}

// hashChildren computes the sha256 of the sorted concatenation of child hashes.
func hashChildren(children []*MerkleNode) []byte {
	if len(children) == 0 {
		return nil
	}
	h := hash.SHA256Pool.Acquire()
	defer hash.SHA256Pool.Release(h)
	for _, c := range children {
		h.Write(c.Hash)
	}
	return h.Sum(nil)
}

// computeRootDigest converts the root node hash to an OCI digest.
func computeRootDigest(root *MerkleNode) digest.Digest {
	if root == nil || len(root.Hash) == 0 {
		return digest.Digest("")
	}
	return digest.NewDigestFromBytes(digest.SHA256, root.Hash)
}

// ─────────────────────────────────────────────────────────────────────────────
// MerkleForest — collection of per-vertex trees
// ─────────────────────────────────────────────────────────────────────────────

// MerkleForest holds the MerkleTrees produced for all vertices in a build graph.
// It is the final output of the engine.
type MerkleForest struct {
	mu    sync.RWMutex
	trees map[VertexID]*MerkleTree
}

// NewMerkleForest returns an empty MerkleForest.
func NewMerkleForest() *MerkleForest {
	return &MerkleForest{trees: make(map[VertexID]*MerkleTree)}
}

// Add stores a finalized MerkleTree.
func (f *MerkleForest) Add(t *MerkleTree) {
	f.mu.Lock()
	f.trees[t.VertexID] = t
	f.mu.Unlock()
}

// Get returns the MerkleTree for the given vertex, or (nil, false).
func (f *MerkleForest) Get(vertexID VertexID) (*MerkleTree, bool) {
	f.mu.RLock()
	t, ok := f.trees[vertexID]
	f.mu.RUnlock()
	return t, ok
}

// All returns all MerkleTrees, sorted by VertexID for deterministic output.
func (f *MerkleForest) All() []*MerkleTree {
	f.mu.RLock()
	out := make([]*MerkleTree, 0, len(f.trees))
	for _, t := range f.trees {
		out = append(out, t)
	}
	f.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].VertexID < out[j].VertexID
	})
	return out
}

// Len returns the number of trees in the forest.
func (f *MerkleForest) Len() int {
	f.mu.RLock()
	n := len(f.trees)
	f.mu.RUnlock()
	return n
}
