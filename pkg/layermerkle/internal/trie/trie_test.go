package trie_test

import (
	"testing"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/trie"
)

func TestNode_Insert_SingleFile(t *testing.T) {
	root := trie.NewRoot()
	root.Insert("usr/bin/sh", []byte("hash"))
	if root.Len() != 1 {
		t.Errorf("Len() = %d, want 1", root.Len())
	}
}

func TestNode_Insert_MultipleFiles(t *testing.T) {
	root := trie.NewRoot()
	paths := []string{"a/b/c", "a/b/d", "a/e", "f"}
	for _, p := range paths {
		root.Insert(p, []byte(p))
	}
	if root.Len() != 4 {
		t.Errorf("Len() = %d, want 4", root.Len())
	}
}

func TestNode_Insert_Overwrite(t *testing.T) {
	root := trie.NewRoot()
	root.Insert("x/y", []byte("v1"))
	root.Insert("x/y", []byte("v2"))
	if root.Len() != 1 {
		t.Errorf("Len() = %d, want 1 after overwrite", root.Len())
	}
}

func TestNode_Walk_LexicographicOrder(t *testing.T) {
	root := trie.NewRoot()
	for _, p := range []string{"z", "a", "m", "b"} {
		root.Insert(p, []byte(p))
	}
	var order []string
	root.Walk(func(path string, _ *trie.Node) bool {
		order = append(order, path)
		return true
	})
	for i := 1; i < len(order); i++ {
		if order[i-1] > order[i] {
			t.Errorf("Walk not in lexicographic order at index %d: %q > %q", i, order[i-1], order[i])
		}
	}
}

func TestNode_Walk_EarlyTermination(t *testing.T) {
	root := trie.NewRoot()
	for _, p := range []string{"a", "b", "c", "d", "e"} {
		root.Insert(p, []byte(p))
	}
	count := 0
	root.Walk(func(_ string, _ *trie.Node) bool {
		count++
		return count < 3 // stop after 3
	})
	if count != 3 {
		t.Errorf("Walk called fn %d times, want 3 (early termination)", count)
	}
}

func TestNode_SortedChildren_Deterministic(t *testing.T) {
	root := trie.NewRoot()
	for _, p := range []string{"z/x", "a/x", "m/x"} {
		root.Insert(p, []byte(p))
	}
	children := root.SortedChildren()
	for i := 1; i < len(children); i++ {
		if children[i-1].Segment > children[i].Segment {
			t.Errorf("SortedChildren not sorted at %d", i)
		}
	}
}
