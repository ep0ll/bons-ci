package merkle

import (
	"fmt"
	"sync"

	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// Registry
// ─────────────────────────────────────────────────────────────────────────────

// Registry manages one MerkleTree per layer. It is the single authoritative
// store of per-layer Merkle state in the layermerkle package.
//
// The Registry is lazily populated: a tree is created on first access to a
// given layer. Trees remain open (unsealed) until Seal() or SealAll() is called
// by the pipeline after an ExecOp completes.
//
// Thread safety: all methods are safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	trees map[layer.Digest]*MerkleTree
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		trees: make(map[layer.Digest]*MerkleTree),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Leaf / deletion management
// ─────────────────────────────────────────────────────────────────────────────

// AddLeaf adds or updates a leaf in the tree for layerDigest.
// Returns true if this is the first time this (layerDigest, filePath) pair
// was added; false if it was updated or if the tree is already sealed.
//
// This method is called by the dedup engine on every successful hash lookup.
// It is safe to call from concurrent goroutines.
func (r *Registry) AddLeaf(layerDigest layer.Digest, filePath string, hash []byte) bool {
	return r.AddLeafWithMeta(layerDigest, filePath, hash, "sha256")
}

// AddLeafWithMeta is like AddLeaf but also accepts the algorithm label.
func (r *Registry) AddLeafWithMeta(
	layerDigest layer.Digest,
	filePath string,
	hash []byte,
	algorithm string,
) bool {
	return r.tree(layerDigest).AddLeaf(filePath, hash, algorithm)
}

// RecordDeletion records that filePath was deleted in layerDigest's tree.
func (r *Registry) RecordDeletion(layerDigest layer.Digest, filePath string) {
	r.tree(layerDigest).RecordDeletion(filePath)
}

// ─────────────────────────────────────────────────────────────────────────────
// Sealing
// ─────────────────────────────────────────────────────────────────────────────

// Seal finalises the tree for layerDigest and returns its Merkle root.
// Subsequent AddLeaf / RecordDeletion calls for layerDigest become no-ops.
// Seal is idempotent.
func (r *Registry) Seal(layerDigest layer.Digest) []byte {
	return r.tree(layerDigest).Seal()
}

// SealAll seals every open tree in parallel and returns a snapshot of
// layer→root mappings. Safe to call concurrently with Run.
func (r *Registry) SealAll() map[layer.Digest][]byte {
	r.mu.RLock()
	digests := make([]layer.Digest, 0, len(r.trees))
	for d := range r.trees {
		digests = append(digests, d)
	}
	r.mu.RUnlock()

	type kv struct {
		d    layer.Digest
		root []byte
	}
	results := make(chan kv, len(digests))

	// Seal trees in parallel — each tree has its own mutex, so there is no
	// contention between seals.
	var wg sync.WaitGroup
	for _, d := range digests {
		d := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- kv{d: d, root: r.Seal(d)}
		}()
	}
	wg.Wait()
	close(results)

	out := make(map[layer.Digest][]byte, len(digests))
	for kv := range results {
		out[kv.d] = kv.root
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Query
// ─────────────────────────────────────────────────────────────────────────────

// Root returns the Merkle root for the (sealed) tree of layerDigest.
// Returns an error if the layer is unknown or the tree is not yet sealed.
func (r *Registry) Root(layerDigest layer.Digest) ([]byte, error) {
	t, ok := r.treeReadOnly(layerDigest)
	if !ok {
		return nil, fmt.Errorf("merkle: no tree registered for layer %q", layerDigest)
	}
	return t.Root()
}

// Proof returns a Merkle inclusion proof for filePath in layerDigest's sealed tree.
func (r *Registry) Proof(layerDigest layer.Digest, filePath string) (*Proof, error) {
	t, ok := r.treeReadOnly(layerDigest)
	if !ok {
		return nil, fmt.Errorf("merkle: no tree registered for layer %q", layerDigest)
	}
	p, err := t.Proof(filePath)
	if err != nil {
		return nil, err
	}
	p.LayerDigest = layerDigest
	return p, nil
}

// Leaves returns the sorted leaf list for the sealed tree of layerDigest.
func (r *Registry) Leaves(layerDigest layer.Digest) ([]Leaf, error) {
	t, ok := r.treeReadOnly(layerDigest)
	if !ok {
		return nil, fmt.Errorf("merkle: no tree registered for layer %q", layerDigest)
	}
	return t.Leaves()
}

// Deletions returns the deleted file paths for layerDigest's tree.
func (r *Registry) Deletions(layerDigest layer.Digest) []string {
	t, ok := r.treeReadOnly(layerDigest)
	if !ok {
		return nil
	}
	return t.Deletions()
}

// LeafCount returns the number of leaves accumulated for layerDigest.
// Works on both sealed and unsealed trees. Returns 0 for unknown layers.
func (r *Registry) LeafCount(layerDigest layer.Digest) int {
	t, ok := r.treeReadOnly(layerDigest)
	if !ok {
		return 0
	}
	return t.Len()
}

// Layers returns the digest of every layer that has a registered tree.
func (r *Registry) Layers() []layer.Digest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]layer.Digest, 0, len(r.trees))
	for d := range r.trees {
		result = append(result, d)
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// tree returns the MerkleTree for layerDigest, creating it if it does not exist.
// Uses a double-checked locking pattern to avoid write lock overhead on the
// hot path (leaf insertions).
func (r *Registry) tree(layerDigest layer.Digest) *MerkleTree {
	// Fast path: read lock.
	r.mu.RLock()
	t, ok := r.trees[layerDigest]
	r.mu.RUnlock()
	if ok {
		return t
	}

	// Slow path: write lock, check again, create.
	r.mu.Lock()
	if t, ok = r.trees[layerDigest]; !ok {
		t = newMerkleTree()
		r.trees[layerDigest] = t
	}
	r.mu.Unlock()
	return t
}

// treeReadOnly returns the tree for layerDigest if it exists.
func (r *Registry) treeReadOnly(layerDigest layer.Digest) (*MerkleTree, bool) {
	r.mu.RLock()
	t, ok := r.trees[layerDigest]
	r.mu.RUnlock()
	return t, ok
}
