// Package state internal tests — exercises branches unreachable via public API.
// These test functions are in package state (not state_test) so they can call
// unexported functions directly.
package state

import (
	"context"
	"path"
	"testing"
)

// ─── cleanPath — branch 2: result[0] != '/' ───────────────────────────────────
// path.Clean of a relative string (e.g. "rel") returns "rel" which does not
// start with '/'. cleanPath must prepend "/" in that case.
// This branch is unreachable via the public Dir() API (which always joins with
// an absolute base first) but exists as a defensive guard.

func TestCleanPathRelativeBranchInternal(t *testing.T) {
	// Direct call to cleanPath with a relative string.
	// path.Clean("rel") = "rel" → branch 2: prepend "/"
	result := cleanPath("rel")
	if result != "/rel" {
		t.Errorf("cleanPath('rel'): want /rel, got %q", result)
	}
}

func TestCleanPathEmptyBranchInternal(t *testing.T) {
	// path.Clean("") = "." → branch 1: return "/"
	result := cleanPath("")
	if result != "/" {
		t.Errorf("cleanPath(''): want /, got %q", result)
	}
}

func TestCleanPathDotBranchInternal(t *testing.T) {
	// path.Clean(".") = "." → branch 1: return "/"
	result := cleanPath(".")
	if result != "/" {
		t.Errorf("cleanPath('.'): want /, got %q", result)
	}
}

func TestCleanPathAbsoluteBranchInternal(t *testing.T) {
	// path.Clean("/foo/bar") = "/foo/bar" → branch 3: return as-is
	result := cleanPath("/foo/bar")
	if result != "/foo/bar" {
		t.Errorf("cleanPath('/foo/bar'): want /foo/bar, got %q", result)
	}
}

// ─── State.GetDir — both branches ────────────────────────────────────────────

func TestGetDirEmptyMetaDirInternal(t *testing.T) {
	// A state where meta.dir == "" → GetDir returns "/"
	s := State{meta: &Meta{}} // meta.dir is ""
	dir, err := s.GetDir(context.Background())
	if err != nil {
		t.Fatalf("GetDir: %v", err)
	}
	if dir != "/" {
		t.Errorf("empty meta.dir: want /, got %q", dir)
	}
}

func TestGetDirNonEmptyMetaDirInternal(t *testing.T) {
	// A state where meta.dir != "" → GetDir returns that dir.
	s := State{meta: &Meta{dir: "/workspace"}}
	dir, err := s.GetDir(context.Background())
	if err != nil {
		t.Fatalf("GetDir: %v", err)
	}
	if dir != "/workspace" {
		t.Errorf("non-empty meta.dir: want /workspace, got %q", dir)
	}
}

// Ensure path import is used to silence linter.
var _ = path.Clean
