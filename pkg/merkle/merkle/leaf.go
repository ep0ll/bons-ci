// Package merkle provides per-layer Merkle tree construction for dedup-
// processed file access events. Each layer accumulates leaves (filepath → hash
// pairs), and when Seal() is called the tree is finalised and its root digest
// is available.
//
// # Tree structure
//
// Leaves are sorted by FilePath for determinism. The leaf hash is:
//
//	leafHash = SHA256(
//	    uint32BE(len(filePath)) || filePath ||
//	    uint32BE(len(contentHash)) || contentHash
//	)
//
// Internal nodes are:
//
//	nodeHash = SHA256(left.Hash || right.Hash)
//
// If a level has an odd number of nodes, the last node is paired with itself
// (the "balanced" convention, also used by Bitcoin and IPFS).
//
// # Inclusion proofs
//
// MerkleTree.Proof returns a Proof that can be verified by ProofVerify without
// access to the full tree. A proof is a list of SiblingStep values plus the
// leaf hash and root hash.
package merkle

import (
	"encoding/binary"
	"fmt"

	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// Leaf
// ─────────────────────────────────────────────────────────────────────────────

// Leaf is a single entry in a layer's Merkle tree. It records one file that
// was accessed during the layer's ExecOp, along with its content hash.
type Leaf struct {
	// FilePath is the absolute path within the merged overlay view.
	FilePath string

	// Hash is the file's content digest (raw bytes, e.g., SHA256).
	Hash []byte

	// Algorithm identifies the hash function, e.g., "sha256".
	Algorithm string

	// SourceLayer is the layer the file content was read from.
	SourceLayer layer.Digest
}

// LeafHash computes the Merkle leaf hash using a length-prefixed encoding
// that prevents second-preimage attacks by making the file path part of the
// hash input. This also ensures that two leaves with the same content hash but
// different file paths produce different leaf hashes.
//
//	SHA256(uint32BE(len(FilePath)) || FilePath || uint32BE(len(Hash)) || Hash)
func (l Leaf) LeafHash() []byte {
	return leafHash(l.FilePath, l.Hash)
}

// leafHash is the internal leaf hash computation, exported as a package-level
// function so the proof verifier can recompute it without a Leaf value.
func leafHash(filePath string, contentHash []byte) []byte {
	// Pre-allocate: 4 + len(path) + 4 + len(hash)
	buf := make([]byte, 4+len(filePath)+4+len(contentHash))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(filePath)))
	copy(buf[4:], filePath)
	off := 4 + len(filePath)
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(len(contentHash)))
	copy(buf[off+4:], contentHash)
	return sha256Bytes(buf)
}

// String returns a compact human-readable representation.
func (l Leaf) String() string {
	hex := fmt.Sprintf("%x", l.Hash)
	if len(hex) > 16 {
		hex = hex[:16] + "…"
	}
	return fmt.Sprintf("Leaf{path=%q hash=%s:%s src=%s}", l.FilePath, l.Algorithm, hex, l.SourceLayer)
}

// ─────────────────────────────────────────────────────────────────────────────
// Proof
// ─────────────────────────────────────────────────────────────────────────────

// Proof is a Merkle inclusion proof for a single leaf. It allows a verifier to
// confirm that a leaf is part of a tree with a known root hash, without access
// to the full tree.
type Proof struct {
	// FilePath is the leaf's file path.
	FilePath string

	// LeafHash is the Merkle leaf hash (not the content hash).
	LeafHash []byte

	// Root is the Merkle root of the tree at Seal time.
	Root []byte

	// Siblings are the sibling hashes from leaf to root. Each step includes
	// the sibling hash and whether it is on the left or right.
	Siblings []SiblingStep

	// LayerDigest is the output layer this proof belongs to.
	LayerDigest layer.Digest
}

// SiblingStep is one step in a Merkle inclusion proof.
type SiblingStep struct {
	// Hash is the sibling node's hash at this level.
	Hash []byte

	// IsLeft is true when the sibling is the LEFT child and the proven node
	// is the RIGHT child. IsLeft == false means sibling is right child.
	IsLeft bool
}

// Verify recomputes the root from the leaf hash and sibling path and compares
// it against Proof.Root. Returns nil if the proof is valid.
func (p *Proof) Verify() error {
	if len(p.LeafHash) == 0 {
		return fmt.Errorf("merkle: proof has empty leaf hash")
	}
	if len(p.Root) == 0 {
		return fmt.Errorf("merkle: proof has empty root")
	}

	current := p.LeafHash
	for i, step := range p.Siblings {
		if len(step.Hash) == 0 {
			return fmt.Errorf("merkle: proof step %d has empty sibling hash", i)
		}
		var left, right []byte
		if step.IsLeft {
			left, right = step.Hash, current
		} else {
			left, right = current, step.Hash
		}
		current = internalNodeHash(left, right)
	}

	if len(current) != len(p.Root) {
		return fmt.Errorf("merkle: computed root length mismatch: got %d, want %d", len(current), len(p.Root))
	}
	for i := range current {
		if current[i] != p.Root[i] {
			return fmt.Errorf("merkle: proof verification failed: root mismatch at byte %d", i)
		}
	}
	return nil
}
