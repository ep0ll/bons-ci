// Package trie provides a path-aware trie (prefix tree) used by the
// MerkleTreeBuilder to accumulate file records and compute intermediate
// directory hashes without redundant string parsing.
package trie

import (
	"path"
	"sort"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Node — one path segment in the trie
// ─────────────────────────────────────────────────────────────────────────────

// Node is one segment in the path trie. Leaf nodes carry file hashes;
// interior nodes represent directories.
type Node struct {
	// Segment is this node's single path component (not the full path).
	Segment string

	// Hash is set for leaf nodes (files) by the caller.
	// For directory nodes it is computed by Digest().
	Hash []byte

	// IsDir reports whether this node represents a directory.
	IsDir bool

	// children are keyed by their Segment for O(1) lookup.
	children map[string]*Node
}

// NewRoot returns an unnamed root directory node.
func NewRoot() *Node {
	return &Node{IsDir: true, children: make(map[string]*Node)}
}

// ─────────────────────────────────────────────────────────────────────────────
// Insert — add or replace a file leaf
// ─────────────────────────────────────────────────────────────────────────────

// Insert adds a file leaf at the given forward-slash path with the given hash.
// Parent directory nodes are created on demand.
// path must be clean and must not start with "/".
func (n *Node) Insert(filePath string, hash []byte) {
	filePath = path.Clean(filePath)
	segments := splitPath(filePath)
	n.insert(segments, hash)
}

func (n *Node) insert(segments []string, hash []byte) {
	if len(segments) == 0 {
		return
	}
	seg := segments[0]
	child, exists := n.children[seg]
	if !exists {
		child = &Node{
			Segment:  seg,
			IsDir:    len(segments) > 1,
			children: make(map[string]*Node),
		}
		n.children[seg] = child
	}
	if len(segments) == 1 {
		child.Hash = hash
		child.IsDir = false
		return
	}
	child.IsDir = true
	child.insert(segments[1:], hash)
}

// ─────────────────────────────────────────────────────────────────────────────
// Len — count of leaf nodes
// ─────────────────────────────────────────────────────────────────────────────

// Len returns the number of file leaves in the subtree rooted at n.
func (n *Node) Len() int {
	if !n.IsDir {
		return 1
	}
	total := 0
	for _, c := range n.children {
		total += c.Len()
	}
	return total
}

// ─────────────────────────────────────────────────────────────────────────────
// Walk — depth-first traversal in sorted order
// ─────────────────────────────────────────────────────────────────────────────

// WalkFn is called for each node during traversal. Return false to stop early.
type WalkFn func(fullPath string, n *Node) bool

// Walk traverses the trie depth-first in lexicographic order, calling fn for
// every node. The root itself is NOT passed to fn.
func (n *Node) Walk(fn WalkFn) {
	n.walk("", fn)
}

func (n *Node) walk(prefix string, fn WalkFn) bool {
	keys := sortedKeys(n.children)
	for _, key := range keys {
		child := n.children[key]
		var fullPath string
		if prefix == "" {
			fullPath = key
		} else {
			fullPath = prefix + "/" + key
		}
		if !fn(fullPath, child) {
			return false
		}
		if child.IsDir {
			if !child.walk(fullPath, fn) {
				return false
			}
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// SortedChildren — deterministic child ordering
// ─────────────────────────────────────────────────────────────────────────────

// SortedChildren returns the direct children of n sorted by Segment.
func (n *Node) SortedChildren() []*Node {
	keys := sortedKeys(n.children)
	result := make([]*Node, len(keys))
	for i, k := range keys {
		result[i] = n.children[k]
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func splitPath(p string) []string {
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}

func sortedKeys(m map[string]*Node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
