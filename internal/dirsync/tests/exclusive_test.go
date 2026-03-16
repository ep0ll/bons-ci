package dirsync_test

// exclusive_test.go – tests for ExclusivePath emission and subtree pruning.

import (
	"os"
	"testing"
)

// TestExclusive_EmptyUpper verifies that when upper is empty every lower entry
// is emitted as exclusive and that directories are pruned (not recursed into).
func TestExclusive_EmptyUpper(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	writeFile(t, lower, "hello", "a.txt")
	writeFile(t, lower, "world", "b.txt")
	mkdir(t, lower, "subdir")
	writeFile(t, lower, "nested", "subdir", "c.txt")
	writeFile(t, lower, "nested2", "subdir", "d.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	// Expect: a.txt, b.txt, subdir (pruned) — NOT subdir/c.txt or subdir/d.txt.
	if len(dr.Common) != 0 {
		t.Errorf("expected 0 common paths, got %d", len(dr.Common))
	}

	relPaths := exclusiveRelPaths(dr.Exclusive)
	t.Logf("exclusive paths: %v", relPaths)

	// subdir must appear as a single pruned entry.
	found := false
	for _, ep := range dr.Exclusive {
		if ep.RelPath == "subdir" {
			if !ep.Pruned {
				t.Errorf("subdir should be Pruned=true")
			}
			if !ep.IsDir {
				t.Errorf("subdir should be IsDir=true")
			}
			found = true
		}
		// Children of subdir must NOT be emitted when parent is pruned.
		if ep.RelPath == "subdir/c.txt" || ep.RelPath == "subdir/d.txt" {
			t.Errorf("child %q emitted even though parent dir is pruned", ep.RelPath)
		}
	}
	if !found {
		t.Errorf("subdir not found in exclusive paths; got: %v", relPaths)
	}

	if !containsRelPath(dr.Exclusive, "a.txt") {
		t.Errorf("a.txt not in exclusive paths")
	}
	if !containsRelPath(dr.Exclusive, "b.txt") {
		t.Errorf("b.txt not in exclusive paths")
	}
}

// TestExclusive_MissingUpperDir verifies pruning when upper subtree is absent.
// lower has: shared/ (in both) and only/ (exclusive).
// We expect only/ to be pruned, and shared/ to be walked (producing common entries).
func TestExclusive_MissingUpperDir(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// Shared subtree.
	writeFile(t, lower, "A", "shared", "file.txt")
	writeFile(t, upper, "A", "shared", "file.txt")

	// Exclusive subtree: only in lower, deeply nested.
	writeFile(t, lower, "X", "only", "deep", "deeper", "file.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	// "only" must appear as a single pruned root — NOT "only/deep" or deeper.
	if !containsRelPath(dr.Exclusive, "only") {
		t.Errorf("expected 'only' in exclusive; got %v", exclusiveRelPaths(dr.Exclusive))
	}
	for _, ep := range dr.Exclusive {
		if ep.RelPath != "only" {
			t.Errorf("unexpected exclusive path %q (only 'only' should be emitted)", ep.RelPath)
		}
	}

	// shared/file.txt must be common.
	if len(dr.Common) == 0 {
		t.Errorf("expected shared/file.txt in common paths")
	}
}

// TestExclusive_InterleavedNames exercises the merge-sort pointer logic when
// lower and upper have a mix of same and different names at the same level.
func TestExclusive_InterleavedNames(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// Lower:  a.txt  b.txt  c.txt  d.txt
	// Upper:          b.txt         d.txt  e.txt
	// Exclusive lower: a.txt, c.txt
	// Common:          b.txt, d.txt
	writeFile(t, lower, "a", "a.txt")
	writeFile(t, lower, "b", "b.txt")
	writeFile(t, lower, "c", "c.txt")
	writeFile(t, lower, "d", "d.txt")

	writeFile(t, upper, "b", "b.txt")
	writeFile(t, upper, "d", "d.txt")
	writeFile(t, upper, "e", "e.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	if !containsRelPath(dr.Exclusive, "a.txt") {
		t.Errorf("a.txt not exclusive")
	}
	if !containsRelPath(dr.Exclusive, "c.txt") {
		t.Errorf("c.txt not exclusive")
	}
	// e.txt is exclusive to upper — must NOT appear in exclusive (lower only).
	if containsRelPath(dr.Exclusive, "e.txt") {
		t.Errorf("e.txt should not be in exclusive (it is upper-only)")
	}
	if len(dr.Exclusive) != 2 {
		t.Errorf("expected 2 exclusive paths, got %d: %v",
			len(dr.Exclusive), exclusiveRelPaths(dr.Exclusive))
	}
	if len(dr.Common) < 2 {
		t.Errorf("expected at least 2 common paths (b.txt, d.txt)")
	}
}

// TestExclusive_TypeMismatch ensures that when lower has a directory but upper
// has a file with the same name, the lower directory is treated as exclusive.
func TestExclusive_TypeMismatch(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// lower: "foo" is a directory with content.
	writeFile(t, lower, "content", "foo", "inner.txt")
	// upper: "foo" is a regular file.
	writeFile(t, upper, "overridden", "foo")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	// lower/foo (dir) must appear as exclusive.
	found := false
	for _, ep := range dr.Exclusive {
		if ep.RelPath == "foo" {
			found = true
			if !ep.IsDir {
				t.Errorf("foo exclusive entry: IsDir should be true")
			}
		}
	}
	if !found {
		t.Errorf("expected 'foo' in exclusive paths; got %v",
			exclusiveRelPaths(dr.Exclusive))
	}
}

// TestExclusive_EmptyLower confirms zero exclusive paths when lower is empty.
func TestExclusive_EmptyLower(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()
	writeFile(t, upper, "data", "only_upper.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	if len(dr.Exclusive) != 0 {
		t.Errorf("expected 0 exclusive, got %d: %v",
			len(dr.Exclusive), exclusiveRelPaths(dr.Exclusive))
	}
}

// TestExclusive_AbsPath verifies that AbsPath is a properly joined absolute
// path pointing into the lower root.
func TestExclusive_AbsPath(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()
	writeFile(t, lower, "x", "sub", "file.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	for _, ep := range dr.Exclusive {
		if ep.RelPath == "sub" {
			// AbsPath must be lower/sub (not lower/sub/file.txt — pruned).
			want := lower + "/sub"
			if ep.AbsPath != want {
				t.Errorf("AbsPath = %q, want %q", ep.AbsPath, want)
			}
			// Confirm the path actually exists on disk.
			if _, err := os.Lstat(ep.AbsPath); err != nil {
				t.Errorf("AbsPath does not exist on disk: %v", err)
			}
		}
	}
}

// TestExclusive_PrunedDirNotRecursed is the primary pruning DSA invariant test:
// a deeply-nested exclusive directory must yield exactly one ExclusivePath entry
// regardless of how many files live inside it.
func TestExclusive_PrunedDirNotRecursed(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	const depth = 5
	const filesPerLevel = 3

	// Build a deeply nested subtree only in lower.
	buildNested(t, lower, "exclusive_root", depth, filesPerLevel)

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	// Despite depth*filesPerLevel+ entries in the subtree, we must get exactly
	// ONE exclusive path: the pruned root.
	if len(dr.Exclusive) != 1 {
		t.Errorf("want 1 exclusive (pruned root), got %d: %v",
			len(dr.Exclusive), exclusiveRelPaths(dr.Exclusive))
	}
	if dr.Exclusive[0].RelPath != "exclusive_root" {
		t.Errorf("wrong exclusive root: %q", dr.Exclusive[0].RelPath)
	}
	if !dr.Exclusive[0].Pruned {
		t.Error("exclusive root should be Pruned=true")
	}
}

// buildNested recursively creates a directory tree of given depth.
func buildNested(t *testing.T, root, name string, depth, filesPerLevel int) {
	t.Helper()
	if depth == 0 {
		return
	}
	dir := mkdir(t, root, name)
	for i := 0; i < filesPerLevel; i++ {
		writeFile(t, dir, "data", "leaf_"+string(rune('a'+i))+".txt")
	}
	buildNested(t, dir, "sub", depth-1, filesPerLevel)
}
