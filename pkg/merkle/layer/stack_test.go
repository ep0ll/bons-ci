package layer_test

import (
	"testing"

	"github.com/user/layermerkle/layer"
)

func TestNew(t *testing.T) {
	s, err := layer.New("a", "b", "c")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.Len() != 3 {
		t.Fatalf("got len %d, want 3", s.Len())
	}
}

func TestNew_Empty(t *testing.T) {
	_, err := layer.New()
	if err == nil {
		t.Fatal("expected error for empty stack")
	}
}

func TestMustNew_Panic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	layer.MustNew()
}

func TestTop(t *testing.T) {
	s := layer.MustNew("a", "b", "c")
	top, err := s.Top()
	if err != nil {
		t.Fatalf("Top: %v", err)
	}
	if top != "c" {
		t.Fatalf("got %q, want %q", top, "c")
	}
}

func TestBase(t *testing.T) {
	s := layer.MustNew("a", "b", "c")
	base, err := s.Base()
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	if base != "a" {
		t.Fatalf("got %q, want %q", base, "a")
	}
}

func TestContains(t *testing.T) {
	s := layer.MustNew("a", "b", "c")
	if !s.Contains("b") {
		t.Fatal("expected Contains(b) = true")
	}
	if s.Contains("z") {
		t.Fatal("expected Contains(z) = false")
	}
}

func TestBelow(t *testing.T) {
	s := layer.MustNew("a", "b", "c", "d")

	below, err := s.Below("c")
	if err != nil {
		t.Fatalf("Below: %v", err)
	}
	if below.Len() != 2 {
		t.Fatalf("got len %d, want 2", below.Len())
	}
	if below[0] != "a" || below[1] != "b" {
		t.Fatalf("got %v, want [a b]", below)
	}
}

func TestBelow_Base(t *testing.T) {
	s := layer.MustNew("a", "b")
	below, err := s.Below("a")
	if err != nil {
		t.Fatalf("Below(base): %v", err)
	}
	if below.Len() != 0 {
		t.Fatalf("expected empty stack, got len %d", below.Len())
	}
}

func TestBelow_NotInStack(t *testing.T) {
	s := layer.MustNew("a", "b")
	_, err := s.Below("z")
	if err == nil {
		t.Fatal("expected error for unknown digest")
	}
}

func TestAncestorsOf(t *testing.T) {
	s := layer.MustNew("base", "exec1", "exec2")
	anc, err := s.AncestorsOf("exec2")
	if err != nil {
		t.Fatalf("AncestorsOf: %v", err)
	}
	if anc.Len() != 2 {
		t.Fatalf("got len %d, want 2", anc.Len())
	}
	if anc[0] != "base" || anc[1] != "exec1" {
		t.Fatalf("got %v, want [base exec1]", anc)
	}
}

func TestPush(t *testing.T) {
	s := layer.MustNew("a", "b")
	s2 := s.Push("c")
	if s2.Len() != 3 {
		t.Fatalf("got len %d, want 3", s2.Len())
	}
	if s.Len() != 2 {
		t.Fatal("Push must not mutate original stack")
	}
}

func TestClone(t *testing.T) {
	s := layer.MustNew("a", "b", "c")
	c := s.Clone()
	if !s.Equal(c) {
		t.Fatal("Clone must be equal to original")
	}
	// Mutating the clone must not affect the original.
	c[0] = "z"
	if s[0] == "z" {
		t.Fatal("Clone shares backing array with original")
	}
}

func TestEqual(t *testing.T) {
	s1 := layer.MustNew("a", "b")
	s2 := layer.MustNew("a", "b")
	s3 := layer.MustNew("a", "c")
	if !s1.Equal(s2) {
		t.Fatal("equal stacks must be Equal")
	}
	if s1.Equal(s3) {
		t.Fatal("different stacks must not be Equal")
	}
}

func TestString(t *testing.T) {
	s := layer.MustNew("a", "b")
	str := s.String()
	if str == "" {
		t.Fatal("String must not return empty string")
	}
}
