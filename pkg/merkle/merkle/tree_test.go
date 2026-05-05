package merkle_test

import (
	"crypto/sha256"
	"testing"

	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/merkle"
)

// ─── MerkleTree ───────────────────────────────────────────────────────────────

func TestEmptyTree(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("l0")
	root := reg.Seal(d)
	empty := sha256.Sum256(nil)
	if string(root) != string(empty[:]) {
		t.Fatal("empty tree root must be sha256(nil)")
	}
}

func TestSingleLeaf(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("l1")
	reg.AddLeaf(d, "/bin/sh", []byte{1, 2, 3})
	root := reg.Seal(d)
	if len(root) != 32 {
		t.Fatalf("expected 32-byte root, got %d", len(root))
	}
}

func TestSealIdempotent(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("l2")
	reg.AddLeaf(d, "/a", []byte{1})
	r1 := reg.Seal(d)
	r2 := reg.Seal(d)
	if string(r1) != string(r2) {
		t.Fatal("Seal must be idempotent")
	}
}

func TestSealFreezes(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("l3")
	reg.AddLeaf(d, "/a", []byte{1})
	r1 := reg.Seal(d)
	// Adding after seal must be a no-op.
	reg.AddLeaf(d, "/b", []byte{2})
	r2, _ := reg.Root(d)
	if string(r1) != string(r2) {
		t.Fatal("AddLeaf after Seal must not change the root")
	}
}

func TestDeterministicRoot(t *testing.T) {
	// Two registries with the same leaves inserted in different order
	// must produce the same root.
	d := layer.Digest("l4")

	reg1 := merkle.NewRegistry()
	reg1.AddLeaf(d, "/z", []byte{1})
	reg1.AddLeaf(d, "/a", []byte{2})
	reg1.AddLeaf(d, "/m", []byte{3})
	r1 := reg1.Seal(d)

	reg2 := merkle.NewRegistry()
	reg2.AddLeaf(d, "/m", []byte{3})
	reg2.AddLeaf(d, "/z", []byte{1})
	reg2.AddLeaf(d, "/a", []byte{2})
	r2 := reg2.Seal(d)

	if string(r1) != string(r2) {
		t.Fatal("same leaves in different order must produce same root")
	}
}

func TestLastWriteWins(t *testing.T) {
	reg1 := merkle.NewRegistry()
	reg2 := merkle.NewRegistry()
	d := layer.Digest("lw")

	reg1.AddLeaf(d, "/f", []byte{1})
	reg1.AddLeaf(d, "/f", []byte{2}) // update
	r1 := reg1.Seal(d)

	reg2.AddLeaf(d, "/f", []byte{2}) // only final value
	r2 := reg2.Seal(d)

	if string(r1) != string(r2) {
		t.Fatal("last-write-wins: duplicate AddLeaf must use final hash")
	}
}

func TestOddLeaves(t *testing.T) {
	// Odd number of leaves exercises the "pair with self" path in buildLevels.
	reg := merkle.NewRegistry()
	d := layer.Digest("lodd")
	for i := 0; i < 5; i++ {
		reg.AddLeaf(d, string(rune('a'+i)), []byte{byte(i)})
	}
	root := reg.Seal(d)
	if len(root) != 32 {
		t.Fatalf("odd-leaf tree: expected 32-byte root, got %d", len(root))
	}
}

func TestRecordDeletion(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("ldel")
	reg.AddLeaf(d, "/keep", []byte{1})
	reg.AddLeaf(d, "/del", []byte{2})
	reg.RecordDeletion(d, "/del")
	reg.Seal(d)

	leaves, err := reg.Leaves(d)
	if err != nil {
		t.Fatalf("Leaves: %v", err)
	}
	if len(leaves) != 1 {
		t.Fatalf("expected 1 leaf after deletion, got %d", len(leaves))
	}
	if leaves[0].FilePath != "/keep" {
		t.Fatalf("wrong leaf: %q", leaves[0].FilePath)
	}
	dels := reg.Deletions(d)
	if len(dels) != 1 || dels[0] != "/del" {
		t.Fatalf("Deletions: expected [/del], got %v", dels)
	}
}

func TestRootBeforeSeal(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("lnoseal")
	reg.AddLeaf(d, "/f", []byte{1})
	_, err := reg.Root(d)
	if err == nil {
		t.Fatal("Root before Seal must return error")
	}
}

// ─── Proof ────────────────────────────────────────────────────────────────────

func TestProofSingleLeaf(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("lproof1")
	reg.AddLeaf(d, "/only", []byte{42})
	reg.Seal(d)

	proof, err := reg.Proof(d, "/only")
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}
	if len(proof.Siblings) != 0 {
		t.Fatalf("single-leaf proof must have 0 siblings, got %d", len(proof.Siblings))
	}
	if err := proof.Verify(); err != nil {
		t.Fatalf("single-leaf proof Verify: %v", err)
	}
}

func TestProofMultiLeaf(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("lproofn")
	files := []string{"/a", "/b", "/c", "/d"}
	for i, f := range files {
		reg.AddLeaf(d, f, []byte{byte(i)})
	}
	reg.Seal(d)

	for _, f := range files {
		proof, err := reg.Proof(d, f)
		if err != nil {
			t.Fatalf("Proof(%q): %v", f, err)
		}
		if err := proof.Verify(); err != nil {
			t.Fatalf("Verify(%q): %v", f, err)
		}
	}
}

func TestProofOddLeaves(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("lproofOdd")
	files := []string{"/a", "/b", "/c"}
	for i, f := range files {
		reg.AddLeaf(d, f, []byte{byte(i + 1)})
	}
	reg.Seal(d)

	for _, f := range files {
		proof, err := reg.Proof(d, f)
		if err != nil {
			t.Fatalf("Proof(%q): %v", f, err)
		}
		if err := proof.Verify(); err != nil {
			t.Fatalf("Verify(%q): %v", f, err)
		}
	}
}

func TestProofUnknownFile(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("lpx")
	reg.AddLeaf(d, "/a", []byte{1})
	reg.Seal(d)

	_, err := reg.Proof(d, "/notexist")
	if err == nil {
		t.Fatal("Proof for unknown file must return error")
	}
}

func TestProofBeforeSeal(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("lnosealf")
	reg.AddLeaf(d, "/f", []byte{1})
	_, err := reg.Proof(d, "/f")
	if err == nil {
		t.Fatal("Proof before Seal must return error")
	}
}

func TestProofTamperedRoot(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("ltamper")
	reg.AddLeaf(d, "/a", []byte{1})
	reg.AddLeaf(d, "/b", []byte{2})
	reg.Seal(d)

	proof, _ := reg.Proof(d, "/a")
	proof.Root[0] ^= 0xFF // flip a byte
	if err := proof.Verify(); err == nil {
		t.Fatal("tampered root must fail Verify")
	}
}

func TestProofTamperedSibling(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("ltamperS")
	for i := 0; i < 4; i++ {
		reg.AddLeaf(d, string(rune('a'+i)), []byte{byte(i)})
	}
	reg.Seal(d)

	proof, _ := reg.Proof(d, "a")
	if len(proof.Siblings) > 0 {
		proof.Siblings[0].Hash[0] ^= 0xFF
		if err := proof.Verify(); err == nil {
			t.Fatal("tampered sibling must fail Verify")
		}
	}
}

// ─── Registry ─────────────────────────────────────────────────────────────────

func TestRegistrySealAll(t *testing.T) {
	reg := merkle.NewRegistry()
	layers := []layer.Digest{"l0", "l1", "l2"}
	for _, d := range layers {
		reg.AddLeaf(d, "/file", []byte{1})
	}
	roots := reg.SealAll()
	if len(roots) != 3 {
		t.Fatalf("SealAll: expected 3 roots, got %d", len(roots))
	}
	for _, d := range layers {
		if roots[d] == nil {
			t.Fatalf("SealAll: missing root for layer %s", d)
		}
	}
}

func TestRegistryUnknownLayer(t *testing.T) {
	reg := merkle.NewRegistry()
	_, err := reg.Root(layer.Digest("nope"))
	if err == nil {
		t.Fatal("Root for unknown layer must return error")
	}
}

func TestRegistryLeafCount(t *testing.T) {
	reg := merkle.NewRegistry()
	d := layer.Digest("lcount")
	for i := 0; i < 7; i++ {
		reg.AddLeaf(d, string(rune('a'+i)), []byte{byte(i)})
	}
	if reg.LeafCount(d) != 7 {
		t.Fatalf("LeafCount: expected 7, got %d", reg.LeafCount(d))
	}
}
