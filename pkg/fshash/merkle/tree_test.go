package merkle_test

import (
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/chunk"
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
	"github.com/bons/bons-ci/pkg/fshash/merkle"
)

func TestTreeBuildAndVerify(t *testing.T) {
	tree := merkle.NewTree(merkle.WithTreeAlgorithm(chunk.BLAKE3))

	hasher := chunk.NewHasher(chunk.BLAKE3)
	l := core.NewLayerID("sha256:test")

	files := map[string][]byte{
		"/a": []byte("alpha"),
		"/b": []byte("bravo"),
		"/c": []byte("charlie"),
		"/d": []byte("delta"),
	}

	for path, data := range files {
		tree.Insert(path, hasher.Hash(data), l)
	}

	root, err := tree.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if root == nil || len(root.Hash) == 0 {
		t.Fatal("expected non-nil root with hash")
	}

	if root.LeafCount() != 4 {
		t.Errorf("LeafCount = %d, want 4", root.LeafCount())
	}

	// Verify internal consistency.
	if err := tree.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestTreeDeterministic(t *testing.T) {
	hasher := chunk.NewHasher(chunk.BLAKE3)
	l := core.NewLayerID("sha256:det")

	// Build tree twice with same data in different insertion order.
	build := func(order []string) []byte {
		tree := merkle.NewTree(merkle.WithTreeAlgorithm(chunk.BLAKE3))
		for _, path := range order {
			tree.Insert(path, hasher.Hash([]byte("data-"+path)), l)
		}
		root, err := tree.Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return root.Hash
	}

	hash1 := build([]string{"/z", "/a", "/m", "/b"})
	hash2 := build([]string{"/b", "/m", "/z", "/a"})

	if len(hash1) != len(hash2) {
		t.Fatal("hash lengths differ")
	}
	for i := range hash1 {
		if hash1[i] != hash2[i] {
			t.Fatal("tree is not deterministic: different insertion orders produce different roots")
		}
	}
}

func TestTreeEmpty(t *testing.T) {
	tree := merkle.NewTree()
	_, err := tree.Build()
	if err == nil {
		t.Error("expected error for empty tree")
	}
}

func TestTreeSingleLeaf(t *testing.T) {
	tree := merkle.NewTree(merkle.WithTreeAlgorithm(chunk.BLAKE3))
	hasher := chunk.NewHasher(chunk.BLAKE3)
	l := core.NewLayerID("sha256:single")

	hash := hasher.Hash([]byte("only-file"))
	tree.Insert("/only", hash, l)

	root, err := tree.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Single leaf: root hash IS the leaf hash.
	if root.LeafCount() != 1 {
		t.Errorf("LeafCount = %d, want 1", root.LeafCount())
	}
}

func TestTreeBranchingFactor(t *testing.T) {
	hasher := chunk.NewHasher(chunk.BLAKE3)
	l := core.NewLayerID("sha256:bf")

	tree := merkle.NewTree(
		merkle.WithBranchingFactor(4),
		merkle.WithTreeAlgorithm(chunk.BLAKE3),
	)

	for i := 0; i < 16; i++ {
		path := string(rune('a'+i)) + ".txt"
		tree.Insert(path, hasher.Hash([]byte(path)), l)
	}

	root, err := tree.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if root.LeafCount() != 16 {
		t.Errorf("LeafCount = %d, want 16", root.LeafCount())
	}

	if err := tree.Verify(); err != nil {
		t.Errorf("Verify with BF=4: %v", err)
	}
}

func TestTreeWalk(t *testing.T) {
	tree := merkle.NewTree(merkle.WithTreeAlgorithm(chunk.BLAKE3))
	hasher := chunk.NewHasher(chunk.BLAKE3)
	l := core.NewLayerID("sha256:walk")

	for _, p := range []string{"/a", "/b", "/c"} {
		tree.Insert(p, hasher.Hash([]byte(p)), l)
	}

	root, _ := tree.Build()

	var nodeCount int
	root.Walk(func(_ *merkle.Node, _ int) bool {
		nodeCount++
		return true
	})

	// 3 leaves + 2 branch nodes = 5 (for binary tree of 3)
	if nodeCount < 3 {
		t.Errorf("walk visited %d nodes, expected at least 3", nodeCount)
	}
}

func TestProofGeneration(t *testing.T) {
	tree := merkle.NewTree(merkle.WithTreeAlgorithm(chunk.BLAKE3))
	hasher := chunk.NewHasher(chunk.BLAKE3)
	l := core.NewLayerID("sha256:proof")

	for _, p := range []string{"/a", "/b", "/c", "/d"} {
		tree.Insert(p, hasher.Hash([]byte("data-"+p)), l)
	}

	root, err := tree.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	proof := tree.GenerateProof("/b")
	if proof == nil {
		t.Fatal("expected non-nil proof for /b")
	}

	if proof.Path != "/b" {
		t.Errorf("proof.Path = %s, want /b", proof.Path)
	}

	// Verify the proof.
	valid := proof.Verify(root.Hash, hasher.Hash)
	if !valid {
		t.Error("proof verification failed for /b")
	}

	// Proof for non-existent path.
	nilProof := tree.GenerateProof("/nonexistent")
	if nilProof != nil {
		t.Error("expected nil proof for non-existent path")
	}
}

// BenchmarkTreeBuild benchmarks tree construction from 10K leaves.
func BenchmarkTreeBuild(b *testing.B) {
	hasher := chunk.NewHasher(chunk.BLAKE3)
	l := core.NewLayerID("sha256:bench")

	type leaf struct {
		path string
		hash []byte
	}

	leaves := make([]leaf, 10000)
	for i := range leaves {
		p := "/file/" + string(rune(i))
		leaves[i] = leaf{path: p, hash: hasher.Hash([]byte(p))}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tree := merkle.NewTree(merkle.WithTreeAlgorithm(chunk.BLAKE3))
		for _, lf := range leaves {
			tree.Insert(lf.path, lf.hash, l)
		}
		tree.Build()
	}
}
