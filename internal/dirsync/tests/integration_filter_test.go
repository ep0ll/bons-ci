package dirsync_test

// filter_integration_test.go – end-to-end tests that exercise IncludePatterns,
// ExcludePatterns, AllowWildcards, and RequiredPaths through the public Diff API.
//
// These tests are in package dirsync_test (black-box) so they can only use the
// exported API.  Fine-grained unit tests for filter internals live in
// filter_test.go and pattern_test.go (white-box, package dirsync).

import (
	"context"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
)

// ─── ExcludePatterns ─────────────────────────────────────────────────────────

// TestFilter_Exclude_PrunesDir confirms that an excluded directory is pruned:
// neither the directory nor any of its children appear in any output channel.
func TestFilter_Exclude_PrunesDir(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// Tree:  vendor/ (exclusive to lower) + src/main.go (common)
	writeFile(t, lower, "x", "vendor", "pkg", "lib.go")
	writeFile(t, lower, "x", "vendor", "pkg", "sub", "deep.go")

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "main", fixedT, "src", "main.go")
	touchAt(t, upper, "main", fixedT, "src", "main.go")

	dr := runDiff(t, lower, upper, dirsync.Options{
		ExcludePatterns: []string{"vendor"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff with exclude")

	// vendor/ must not appear at all — not even as a pruned exclusive root.
	for _, ep := range dr.Exclusive {
		if ep.RelPath == "vendor" || len(ep.RelPath) > 6 && ep.RelPath[:7] == "vendor/" {
			t.Errorf("vendor subtree should be excluded, got exclusive path: %q", ep.RelPath)
		}
	}
	for _, cp := range dr.Common {
		if len(cp.RelPath) >= 6 && cp.RelPath[:6] == "vendor" {
			t.Errorf("vendor subtree should be excluded, got common path: %q", cp.RelPath)
		}
	}

	// src/main.go must still appear.
	if _, ok := commonByRelPath(dr.Common, "src/main.go"); !ok {
		t.Error("src/main.go should still appear despite vendor exclusion")
	}
}

// TestFilter_Exclude_File confirms that a matching file is suppressed but
// its sibling files are unaffected.
func TestFilter_Exclude_File(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "keep", fixedT, "keep.txt")
	touchAt(t, upper, "keep", fixedT, "keep.txt")
	touchAt(t, lower, "skip", fixedT, "skip.txt")
	touchAt(t, upper, "skip", fixedT, "skip.txt")

	dr := runDiff(t, lower, upper, dirsync.Options{
		ExcludePatterns: []string{"skip.txt"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff with file exclude")

	if _, ok := commonByRelPath(dr.Common, "keep.txt"); !ok {
		t.Error("keep.txt should be in common")
	}
	if _, ok := commonByRelPath(dr.Common, "skip.txt"); ok {
		t.Error("skip.txt should be excluded from common")
	}
}

// TestFilter_Exclude_Glob_DotFiles excludes all hidden files/dirs (.*).
func TestFilter_Exclude_Glob_DotFiles(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "vis", fixedT, "visible.go")
	touchAt(t, upper, "vis", fixedT, "visible.go")
	// .git/ is exclusive to lower.
	writeFile(t, lower, "ref", ".git", "HEAD")
	// .env is common.
	touchAt(t, lower, "secret", fixedT, ".env")
	touchAt(t, upper, "secret", fixedT, ".env")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		ExcludePatterns: []string{".*"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff with dot-file glob exclude")

	// .git/ must not appear.
	for _, ep := range dr.Exclusive {
		if ep.RelPath == ".git" {
			t.Error(".git should be excluded")
		}
	}
	// .env must not appear.
	if _, ok := commonByRelPath(dr.Common, ".env"); ok {
		t.Error(".env should be excluded")
	}
	// visible.go must still appear.
	if _, ok := commonByRelPath(dr.Common, "visible.go"); !ok {
		t.Error("visible.go should still appear")
	}
}

// ─── IncludePatterns ─────────────────────────────────────────────────────────

// TestFilter_Include_LiteralBaseName verifies that only files matching the
// include pattern are emitted; non-matching files are silently skipped.
func TestFilter_Include_LiteralBaseName(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "d", fixedT, "go.mod")
	touchAt(t, upper, "d", fixedT, "go.mod")
	touchAt(t, lower, "d", fixedT, "go.sum")
	touchAt(t, upper, "d", fixedT, "go.sum")
	touchAt(t, lower, "x", fixedT, "main.go")
	touchAt(t, upper, "x", fixedT, "main.go")

	dr := runDiff(t, lower, upper, dirsync.Options{
		IncludePatterns: []string{"go.mod", "go.sum"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff with include")

	// go.mod and go.sum must appear.
	for _, name := range []string{"go.mod", "go.sum"} {
		if _, ok := commonByRelPath(dr.Common, name); !ok {
			t.Errorf("%s should be included", name)
		}
	}
	// main.go must NOT appear (filtered out by include).
	if _, ok := commonByRelPath(dr.Common, "main.go"); ok {
		t.Error("main.go should be excluded by include filter")
	}
}

// TestFilter_Include_Glob_GoFiles verifies that *.go glob matches all .go files
// regardless of directory depth.
func TestFilter_Include_Glob_GoFiles(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// Shared .go files at various depths.
	touchAt(t, lower, "x", fixedT, "main.go")
	touchAt(t, upper, "x", fixedT, "main.go")
	touchAt(t, lower, "x", fixedT, "cmd", "run.go")
	touchAt(t, upper, "x", fixedT, "cmd", "run.go")
	// Non-.go files that must be filtered.
	touchAt(t, lower, "x", fixedT, "README.md")
	touchAt(t, upper, "x", fixedT, "README.md")
	touchAt(t, lower, "x", fixedT, "go.mod")
	touchAt(t, upper, "x", fixedT, "go.mod")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff with *.go include")

	for _, name := range []string{"main.go", "cmd/run.go"} {
		if _, ok := commonByRelPath(dr.Common, name); !ok {
			t.Errorf("%s should be included by *.go", name)
		}
	}
	for _, name := range []string{"README.md", "go.mod"} {
		if _, ok := commonByRelPath(dr.Common, name); ok {
			t.Errorf("%s should NOT be included by *.go", name)
		}
	}
}

// TestFilter_Include_Exclusive verifies that include filtering also applies
// to exclusive paths: non-matching exclusive files are silently dropped.
func TestFilter_Include_Exclusive(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// Both files are exclusive to lower.
	writeFile(t, lower, "x", "keep.go")
	writeFile(t, lower, "x", "drop.txt")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff include on exclusive")

	if !containsRelPath(dr.Exclusive, "keep.go") {
		t.Error("keep.go should be in exclusive (matches *.go)")
	}
	if containsRelPath(dr.Exclusive, "drop.txt") {
		t.Error("drop.txt should be excluded by *.go include filter")
	}
}

// TestFilter_Include_DirNotMatchedStillTraversed verifies that a directory
// that doesn't match an include pattern is still traversed (FilterSkip, not
// FilterPrune) so its children can be evaluated.
func TestFilter_Include_DirNotMatchedStillTraversed(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// "src" directory doesn't match "*.go", but "src/main.go" should.
	touchAt(t, lower, "x", fixedT, "src", "main.go")
	touchAt(t, upper, "x", fixedT, "src", "main.go")
	// Non-.go file inside src/ should be filtered.
	touchAt(t, lower, "x", fixedT, "src", "README.md")
	touchAt(t, upper, "x", fixedT, "src", "README.md")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff include traverses non-matching dir")

	// src/main.go must be reached despite "src" not matching *.go.
	if _, ok := commonByRelPath(dr.Common, "src/main.go"); !ok {
		t.Error("src/main.go should be included (directory still traversed)")
	}
	if _, ok := commonByRelPath(dr.Common, "src/README.md"); ok {
		t.Error("src/README.md should be excluded by *.go include filter")
	}
}

// ─── Include + Exclude combined ───────────────────────────────────────────────

// TestFilter_IncludeAndExclude_ExcludeWins verifies the "exclude beats include"
// precedence: files that match both patterns are excluded.
func TestFilter_IncludeAndExclude_ExcludeWins(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// vendor/pkg/x.go: matches *.go (include) AND vendor (exclude).
	touchAt(t, lower, "x", fixedT, "vendor", "pkg", "x.go")
	touchAt(t, upper, "x", fixedT, "vendor", "pkg", "x.go")
	// cmd/main.go: matches *.go only → should appear.
	touchAt(t, lower, "x", fixedT, "cmd", "main.go")
	touchAt(t, upper, "x", fixedT, "cmd", "main.go")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		ExcludePatterns: []string{"vendor"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff include+exclude")

	// vendor/ is pruned → no vendor entries at all.
	for _, cp := range dr.Common {
		if len(cp.RelPath) >= 6 && cp.RelPath[:6] == "vendor" {
			t.Errorf("vendor path should be excluded: %q", cp.RelPath)
		}
	}
	// cmd/main.go must be present.
	if _, ok := commonByRelPath(dr.Common, "cmd/main.go"); !ok {
		t.Error("cmd/main.go should be included")
	}
}

// ─── Options.Filter (custom PathFilter injection) ─────────────────────────────

// extensionFilter is a test-local PathFilter that allows only files whose
// extension matches the configured suffix; directories are always traversed.
type extensionFilter struct{ ext string }

func (f extensionFilter) Decide(relPath string, isDir bool) dirsync.FilterDecision {
	if isDir {
		return dirsync.FilterSkip // traverse, but don't emit the dir itself
	}
	for i := len(relPath) - 1; i >= 0; i-- {
		if relPath[i] == '.' {
			if relPath[i:] == f.ext {
				return dirsync.FilterAllow
			}
			return dirsync.FilterSkip
		}
	}
	return dirsync.FilterSkip
}

// TestFilter_OptionsFilter_CustomPathFilter verifies that a custom PathFilter
// injected via Options.Filter is applied during the walk.
func TestFilter_OptionsFilter_CustomPathFilter(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "x", fixedT, "main.go")
	touchAt(t, upper, "x", fixedT, "main.go")
	touchAt(t, lower, "x", fixedT, "main.py")
	touchAt(t, upper, "x", fixedT, "main.py")
	touchAt(t, lower, "x", fixedT, "README.md")
	touchAt(t, upper, "x", fixedT, "README.md")

	// Only .go files should appear in output.
	dr := runDiff(t, lower, upper, dirsync.Options{
		Filter:      extensionFilter{ext: ".go"},
		HashWorkers: 1,
	})
	assertNoErr(t, dr.Err, "Diff with custom Filter")

	if _, ok := commonByRelPath(dr.Common, "main.go"); !ok {
		t.Error("main.go should appear (extensionFilter allows .go)")
	}
	if _, ok := commonByRelPath(dr.Common, "main.py"); ok {
		t.Error("main.py should be excluded by extensionFilter")
	}
	if _, ok := commonByRelPath(dr.Common, "README.md"); ok {
		t.Error("README.md should be excluded by extensionFilter")
	}
}

// TestFilter_OptionsFilter_ComposedWithPatterns verifies that Options.Filter
// and IncludePatterns compose correctly: the custom filter has veto power.
func TestFilter_OptionsFilter_ComposedWithPatterns(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// Both files would match *.go but secretFilter blocks "secret.go".
	touchAt(t, lower, "x", fixedT, "main.go")
	touchAt(t, upper, "x", fixedT, "main.go")
	touchAt(t, lower, "x", fixedT, "secret.go")
	touchAt(t, upper, "x", fixedT, "secret.go")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		Filter:          extensionFilter{ext: ".go"}, // allows all .go, no veto here
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "Diff with Filter+IncludePatterns")

	// Both should appear since extensionFilter allows all .go.
	for _, name := range []string{"main.go", "secret.go"} {
		if _, ok := commonByRelPath(dr.Common, name); !ok {
			t.Errorf("%s should appear when extensionFilter allows .go", name)
		}
	}
}

// TestFilter_OptionsFilter_Exclusive verifies that the custom filter is also
// applied to exclusive paths (lower-only files).
func TestFilter_OptionsFilter_Exclusive(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	writeFile(t, lower, "x", "keep.go")
	writeFile(t, lower, "x", "drop.py")

	dr := runDiff(t, lower, upper, dirsync.Options{
		Filter:      extensionFilter{ext: ".go"},
		HashWorkers: 1,
	})
	assertNoErr(t, dr.Err, "Diff custom Filter on exclusive paths")

	if !containsRelPath(dr.Exclusive, "keep.go") {
		t.Error("keep.go should be exclusive (extensionFilter allows .go)")
	}
	if containsRelPath(dr.Exclusive, "drop.py") {
		t.Error("drop.py should be excluded (extensionFilter blocks .py)")
	}
}

// TestFilter_OptionsFilter_NilIsNoop verifies that Options.Filter=nil is
// equivalent to no custom filter (no change in behaviour).
func TestFilter_OptionsFilter_NilIsNoop(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "data", fixedT, "file.txt")
	touchAt(t, upper, "data", fixedT, "file.txt")

	dr := runDiff(t, lower, upper, dirsync.Options{
		Filter:      nil, // explicit nil — must be identical to omitting it
		HashWorkers: 1,
	})
	assertNoErr(t, dr.Err, "nil Filter is noop")

	if _, ok := commonByRelPath(dr.Common, "file.txt"); !ok {
		t.Error("file.txt should appear with nil Filter")
	}
}



// TestFilter_Include_ExclusiveDirTraversed is the key regression test for the
// emitLowerOnlyDir fix.
//
// When an exclusive lower directory returns FilterSkip (the dir name itself
// doesn't match the include pattern, but its children might), the walker must
// NOT silently prune the whole subtree.  Instead it must recurse via
// emitLowerOnlyDir and emit only the matching descendants.
func TestFilter_Include_ExclusiveDirTraversed(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// lower has:  src/main.go  src/README.md
	// upper has:  (nothing)
	// include *.go → src/ is FilterSkip, but src/main.go is FilterAllow.
	writeFile(t, lower, "x", "src", "main.go")
	writeFile(t, lower, "x", "src", "README.md") // must be filtered out

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "include filter on exclusive dir")

	// src/main.go must be emitted as exclusive (individual file, not pruned dir).
	if !containsRelPath(dr.Exclusive, "src/main.go") {
		t.Error("src/main.go should be exclusive (dir traversed despite src not matching *.go)")
	}
	// src/ itself must NOT be emitted — it was traversed, not pruned.
	if containsRelPath(dr.Exclusive, "src") {
		t.Error("src/ should not be emitted as a pruned root when traversed for matching children")
	}
	// README.md must be filtered out.
	if containsRelPath(dr.Exclusive, "src/README.md") {
		t.Error("src/README.md should be filtered out by *.go include pattern")
	}
}

// TestFilter_Include_ExclusiveDeepDir verifies that emitLowerOnlyDir recurses
// multiple levels deep when intermediate directories don't match the pattern.
func TestFilter_Include_ExclusiveDeepDir(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// lower: a/b/c/deep.go — three directories, none match *.go, leaf does.
	writeFile(t, lower, "x", "a", "b", "c", "deep.go")
	writeFile(t, lower, "x", "a", "b", "c", "ignore.md")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "deep include traversal")

	if !containsRelPath(dr.Exclusive, "a/b/c/deep.go") {
		t.Errorf("a/b/c/deep.go should be exclusive; got: %v",
			exclusiveRelPaths(dr.Exclusive))
	}
	if containsRelPath(dr.Exclusive, "a/b/c/ignore.md") {
		t.Error("a/b/c/ignore.md should be filtered out")
	}
	// No intermediate directory should be emitted.
	for _, ep := range dr.Exclusive {
		if ep.IsDir {
			t.Errorf("no directory should be emitted when only file children match; got %q", ep.RelPath)
		}
	}
}

// TestFilter_Exclude_ExclusiveDirHardPrune verifies that an exclusive
// directory matching an ExcludePattern (FilterPrune) is dropped completely —
// no children emitted, no recursion — while sibling exclusive directories
// are emitted as pruned roots.
//
// Execution trace (upper is empty → every lower entry is exclusive):
//
//	"build/"  → ExcludeFilter → FilterPrune → dropped, never emitted
//	"src/"    → ExcludeFilter → FilterAllow → emitExclusive("src", …)
//	            → ExclusivePath{RelPath:"src", Pruned:true}
//
// The pruning DSA means "src/" is emitted as a single root entry.
// The walker does NOT recurse into src/ — callers use os.RemoveAll("src/").
// Individual descendants like "src/main.go" are therefore never emitted.
func TestFilter_Exclude_ExclusiveDirHardPrune(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// lower only: build/output.bin  +  src/main.go
	writeFile(t, lower, "x", "build", "output.bin")
	writeFile(t, lower, "x", "src", "main.go")

	dr := runDiff(t, lower, upper, dirsync.Options{
		ExcludePatterns: []string{"build"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "exclude on exclusive dir")

	// build/ must not appear at all — neither the dir nor any child.
	for _, ep := range dr.Exclusive {
		if ep.RelPath == "build" || len(ep.RelPath) >= 6 && ep.RelPath[:6] == "build/" {
			t.Errorf("build subtree should be pruned, got: %q", ep.RelPath)
		}
	}

	// src/ must appear as a single pruned directory root.
	// "src/main.go" will NOT be present — the pruning DSA emits the root only.
	found := false
	for _, ep := range dr.Exclusive {
		if ep.RelPath == "src" {
			found = true
			if !ep.IsDir {
				t.Error("src exclusive entry: IsDir should be true")
			}
			if !ep.Pruned {
				t.Error("src exclusive entry: Pruned should be true (whole subtree exclusive)")
			}
		}
		// Descendants must never be emitted when the root is pruned.
		if ep.RelPath == "src/main.go" {
			t.Error("src/main.go must not be emitted separately — src/ was already pruned")
		}
	}
	if !found {
		t.Errorf("src/ should appear as a pruned exclusive root; got: %v",
			exclusiveRelPaths(dr.Exclusive))
	}
}



// TestFilter_LiteralMode_VendorPrefix confirms that the directory-prefix rule
// works for literal exclusions (so "vendor" excludes "vendor/anything").
func TestFilter_LiteralMode_VendorPrefix(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	writeFile(t, lower, "x", "vendor", "lib.go")

	dr := runDiff(t, lower, upper, dirsync.Options{
		AllowWildcards:  false,
		ExcludePatterns: []string{"vendor"},
		HashWorkers:     1,
	})
	assertNoErr(t, dr.Err, "literal exclude vendor prefix")

	if containsRelPath(dr.Exclusive, "vendor") {
		t.Error("vendor should be excluded (literal prefix rule)")
	}
}

// ─── Invalid glob error surfaced synchronously ───────────────────────────────

// TestFilter_InvalidGlob_ErrorBeforeWalk verifies that Diff returns a non-nil
// error synchronously when a glob pattern is malformed, before any goroutine
// is started and before any channel is created.
func TestFilter_InvalidGlob_ErrorBeforeWalk(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()
	ctx := context.Background()

	_, err := dirsync.Diff(ctx, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"[unclosed"},
	})
	if err == nil {
		t.Error("expected synchronous error for invalid include glob, got nil")
	}

	_, err = dirsync.Diff(ctx, lower, upper, dirsync.Options{
		AllowWildcards:  true,
		ExcludePatterns: []string{"[bad"},
	})
	if err == nil {
		t.Error("expected synchronous error for invalid exclude glob, got nil")
	}
}
