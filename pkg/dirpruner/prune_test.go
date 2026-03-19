package dirprune_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	dirsync "github.com/bons/bons-ci/internal/dirsync"
	dp "github.com/bons/bons-ci/pkg/dirpruner"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// fixture writes a set of files into root. Keys are relative paths (forward
// slash). A trailing "/" in the key creates a directory instead of a file.
func fixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("fixture MkdirAll: %v", err)
		}
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(abs, 0o755); err != nil {
				t.Fatalf("fixture mkdir %s: %v", rel, err)
			}
			continue
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("fixture write %s: %v", rel, err)
		}
	}
}

// ls returns a sorted slice of all relative paths inside root (files and dirs).
func ls(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(abs string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, abs)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("ls %q: %v", root, err)
	}
	sort.Strings(paths)
	return paths
}

func mustExist(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s: %q must exist: %v", label, path, err)
	}
}

func mustNotExist(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %q must not exist", label, path)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Core behaviour
// ─────────────────────────────────────────────────────────────────────────────

// TestPrune_NoFilter_DeletesNothing verifies that a Pruner with no filter
// rules (NoopFilter) leaves the directory completely untouched.
func TestPrune_NoFilter_DeletesNothing(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"a.txt":     "a",
		"sub/b.txt": "b",
	})

	result, err := dp.New().Prune(context.Background(), dir)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(dir, "a.txt"), "a.txt")
	mustExist(t, filepath.Join(dir, "sub/b.txt"), "sub/b.txt")

	if result.Deleted != 0 || result.Collapsed != 0 {
		t.Errorf("expected no deletions, got deleted=%d collapsed=%d",
			result.Deleted, result.Collapsed)
	}
	if result.Kept != 2 {
		t.Errorf("Kept: got %d, want 2 (a.txt + b.txt)", result.Kept)
	}
}

// TestPrune_IncludePatterns_DeletesNonMatching verifies the primary contract:
// only paths matching IncludePatterns are kept; everything else is deleted.
func TestPrune_IncludePatterns_DeletesNonMatching(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"keep.go":        "go",
		"delete.txt":     "del",
		"sub/keep2.go":   "go2",
		"sub/delete2.sh": "del",
		"vendor/dep.go":  "dep", // entire vendor dir will be excluded
	})

	result, err := dp.New(
		dp.WithAllowWildcards(true),
		dp.WithIncludePatterns("*.go"),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(dir, "keep.go"), "keep.go")
	mustExist(t, filepath.Join(dir, "sub/keep2.go"), "sub/keep2.go")
	mustNotExist(t, filepath.Join(dir, "delete.txt"), "delete.txt")
	mustNotExist(t, filepath.Join(dir, "sub/delete2.sh"), "sub/delete2.sh")

	// "vendor" has no .go files at this level that match directly — whether it's
	// kept or collapsed depends on whether *.go could match under it.
	// PatternFilter with wildcards allows descent into any dir for wildcard patterns.
}

// TestPrune_ExcludePatterns_DeletesMatchingExcludes verifies that paths
// matching ExcludePatterns are deleted even if they would match an include.
func TestPrune_ExcludePatterns_DeletesMatchingExcludes(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"main.go":        "main",
		"main_test.go":   "test",
		"util/util.go":   "util",
		"vendor/dep.go":  "dep",
	})

	result, err := dp.New(
		dp.WithAllowWildcards(true),
		dp.WithIncludePatterns("*.go"),
		dp.WithExcludePatterns("vendor", "*_test.go"),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(dir, "main.go"), "main.go (kept)")
	mustExist(t, filepath.Join(dir, "util/util.go"), "util.go (kept)")
	mustNotExist(t, filepath.Join(dir, "main_test.go"), "main_test.go (excluded)")
	mustNotExist(t, filepath.Join(dir, "vendor"), "vendor/ (excluded by prefix)")
}

// TestPrune_ExactPrefixPattern_NoWildcards exercises non-wildcard exact prefix
// matching: "src" keeps "src", "src/main.go", etc.
func TestPrune_ExactPrefixPattern_NoWildcards(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"src/main.go":   "main",
		"src/util.go":   "util",
		"docs/readme.md": "docs",
		"Makefile":       "make",
	})

	result, err := dp.New(
		dp.WithIncludePatterns("src"),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(dir, "src/main.go"), "src/main.go")
	mustExist(t, filepath.Join(dir, "src/util.go"), "src/util.go")
	mustNotExist(t, filepath.Join(dir, "docs"), "docs/ deleted")
	mustNotExist(t, filepath.Join(dir, "Makefile"), "Makefile deleted")
}

// TestPrune_CollapsedDir_SingleOp verifies that an entire non-matching
// directory subtree is deleted as a single collapsed op (no recursion).
func TestPrune_CollapsedDir_SingleOp(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"keep.go":           "keep",
		"vendor/a/b/c.txt":  "del",
		"vendor/a/b/d.txt":  "del",
		"vendor/x.txt":      "del",
	})

	// Use a RecordingBatcher to count ops.
	rb := &dirsync.RecordingBatcher{}
	result, err := dp.New(
		dp.WithIncludePatterns("keep.go"),
		dp.WithBatcher(func(_ dirsync.MergedView) (dirsync.Batcher, error) {
			return rb, nil
		}),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// "vendor" should be a single OpRemoveAll, not per-file ops.
	ops := rb.Ops()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op (collapsed vendor/), got %d: %v", len(ops), ops)
	}
	if ops[0].RelPath != "vendor" {
		t.Errorf("expected op for 'vendor', got %q", ops[0].RelPath)
	}
	if ops[0].Kind != dirsync.OpRemoveAll {
		t.Errorf("collapsed dir must use OpRemoveAll, got %v", ops[0].Kind)
	}

	if result.Collapsed != 1 {
		t.Errorf("Collapsed: got %d, want 1", result.Collapsed)
	}
	if result.Kept != 1 {
		t.Errorf("Kept: got %d, want 1 (keep.go)", result.Kept)
	}
}

// TestPrune_MixedScenario is the canonical integration test covering all
// dispositions simultaneously.
func TestPrune_MixedScenario(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		// kept: matches include "src"
		"src/main.go":     "main",
		"src/util.go":     "util",
		// deleted: individual files that don't match
		"Makefile":         "make",
		"readme.md":        "readme",
		// collapsed: entire subtree doesn't match → single OpRemoveAll
		"vendor/dep/a.go": "dep",
		"vendor/dep/b.go": "dep",
		"cache/x":         "cache",
	})

	result, err := dp.New(
		dp.WithIncludePatterns("src"),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(dir, "src/main.go"), "src/main.go kept")
	mustExist(t, filepath.Join(dir, "src/util.go"), "src/util.go kept")
	mustNotExist(t, filepath.Join(dir, "Makefile"), "Makefile deleted")
	mustNotExist(t, filepath.Join(dir, "readme.md"), "readme.md deleted")
	mustNotExist(t, filepath.Join(dir, "vendor"), "vendor/ collapsed")
	mustNotExist(t, filepath.Join(dir, "cache"), "cache/ collapsed")

	if result.Kept != 2 {
		t.Errorf("Kept: got %d, want 2", result.Kept)
	}
	if result.Deleted != 2 {
		t.Errorf("Deleted: got %d, want 2 (Makefile + readme.md)", result.Deleted)
	}
	if result.Collapsed != 2 {
		t.Errorf("Collapsed: got %d, want 2 (vendor + cache)", result.Collapsed)
	}
}

// TestPrune_RequiredPaths_AbsentStopsPrematurely verifies that missing required
// paths cause Prune to return an error before any deletions occur.
func TestPrune_RequiredPaths_AbsentStopsPrematurely(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{"delete.txt": "del"})

	result, err := dp.New(
		dp.WithRequiredPaths("must-exist.txt"),
	).Prune(context.Background(), dir)

	if err == nil {
		t.Fatal("expected error for missing required path, got nil")
	}
	if !strings.Contains(err.Error(), "must-exist.txt") {
		t.Errorf("error should name the missing path, got: %v", err)
	}

	// The result must be zero — no deletions should have happened.
	if result.Total() != 0 {
		t.Errorf("no actions should have been taken, got Total=%d", result.Total())
	}

	// The file must still be present — no deletions occurred.
	mustExist(t, filepath.Join(dir, "delete.txt"), "delete.txt (no deletions)")
}

// TestPrune_RequiredPaths_PresentProceedsNormally verifies that present required
// paths do not block normal operation.
func TestPrune_RequiredPaths_PresentProceedsNormally(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"required.txt": "req",
		"delete.txt":   "del",
	})

	result, err := dp.New(
		dp.WithRequiredPaths("required.txt"),
		dp.WithIncludePatterns("required.txt"),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(dir, "required.txt"), "required.txt kept")
	mustNotExist(t, filepath.Join(dir, "delete.txt"), "delete.txt deleted")
}

// TestPrune_EmptyDirectory_NoOps verifies that pruning an empty directory
// produces no errors and zero operations.
func TestPrune_EmptyDirectory_NoOps(t *testing.T) {
	dir := t.TempDir()

	result, err := dp.New(
		dp.WithIncludePatterns("*.go"),
		dp.WithAllowWildcards(true),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune empty dir: %v / %v", err, result.Err)
	}
	if result.Total() != 0 {
		t.Errorf("empty dir: expected 0 total, got %d", result.Total())
	}
}

// TestPrune_AllExcluded_DeletesEverything verifies that when all entries are
// excluded (e.g. ExcludePatterns matches everything), the entire tree is
// deleted — each top-level entry as a single collapsed op.
func TestPrune_AllExcluded_DeletesEverything(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"a/x.txt": "x",
		"b/y.txt": "y",
		"c.txt":   "c",
	})

	// Exclude everything explicitly.
	result, err := dp.New(
		dp.WithExcludePatterns("a", "b", "c.txt"),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	entries := ls(t, dir)
	if len(entries) != 0 {
		t.Errorf("expected empty directory after excluding everything, got: %v", entries)
	}

	if result.Collapsed != 2 {
		t.Errorf("Collapsed: got %d, want 2 (a/, b/)", result.Collapsed)
	}
	if result.Deleted != 1 {
		t.Errorf("Deleted: got %d, want 1 (c.txt)", result.Deleted)
	}
}

// TestPrune_DeepNesting_PartialSubtreeMatch verifies correct handling of
// a deep tree where some paths match and others don't within the same subtree.
func TestPrune_DeepNesting_PartialSubtreeMatch(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"pkg/api/handler.go":   "handler",
		"pkg/api/handler_test.go": "test",
		"pkg/api/docs/":        "",
		"pkg/api/docs/api.md":  "docs",
		"pkg/cmd/main.go":      "main",
		"pkg/internal/util.go": "util",
	})

	// Keep only *.go files, exclude test files and docs dirs.
	result, err := dp.New(
		dp.WithAllowWildcards(true),
		dp.WithIncludePatterns("*.go"),
		dp.WithExcludePatterns("*_test.go", "docs"),
	).Prune(context.Background(), dir)

	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(dir, "pkg/api/handler.go"), "handler.go kept")
	mustExist(t, filepath.Join(dir, "pkg/cmd/main.go"), "main.go kept")
	mustExist(t, filepath.Join(dir, "pkg/internal/util.go"), "util.go kept")
	mustNotExist(t, filepath.Join(dir, "pkg/api/handler_test.go"), "test file deleted")
	mustNotExist(t, filepath.Join(dir, "pkg/api/docs"), "docs/ deleted")
}

// TestPrune_ValidationErrors verifies input validation for empty targetDir
// and non-existent / non-directory targets.
func TestPrune_ValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		targetDir string
		wantErr   string
	}{
		{"empty path", "", "must not be empty"},
		{"non-existent", "/no/such/directory/exists/xyz", "stat target"},
		{"is a file", func() string {
			f, _ := os.CreateTemp("", "dirprune-test-*")
			f.Close()
			return f.Name()
		}(), "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dp.New().Prune(context.Background(), tt.targetDir)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected %q in error, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestPrune_ContextCancellation verifies that Prune terminates promptly on
// context cancellation without hanging goroutines.
func TestPrune_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		fixture(t, dir, map[string]string{fmt.Sprintf("f%03d.txt", i): "x"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		dp.New().Prune(ctx, dir) //nolint:errcheck
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Prune did not return after context cancellation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Observer tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPrune_CountingObserver(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"keep.go":       "keep",
		"delete.txt":    "del",
		"collapse/x.sh": "sh",
	})

	counter := &dp.CountingObserver{}
	_, err := dp.New(
		dp.WithIncludePatterns("keep.go"),
		dp.WithObserver(counter),
	).Prune(context.Background(), dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if counter.Kept.Load() != 1 {
		t.Errorf("Kept: got %d, want 1", counter.Kept.Load())
	}
	if counter.Deleted.Load() != 1 {
		t.Errorf("Deleted: got %d, want 1 (delete.txt)", counter.Deleted.Load())
	}
	if counter.Collapsed.Load() != 1 {
		t.Errorf("Collapsed: got %d, want 1 (collapse/)", counter.Collapsed.Load())
	}
}

func TestPrune_LogObserver_WritesLines(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"keep.go":    "k",
		"delete.txt": "d",
	})

	var buf bytes.Buffer
	log := dp.NewLogObserver(&buf, dp.LogAll)
	_, err := dp.New(
		dp.WithIncludePatterns("keep.go"),
		dp.WithObserver(log),
	).Prune(context.Background(), dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "KEPT") {
		t.Errorf("LogAll must log kept paths, got:\n%s", out)
	}
	if !strings.Contains(out, "DELETED") {
		t.Errorf("LogAll must log deleted paths, got:\n%s", out)
	}
}

func TestPrune_MultiObserver(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{"a.txt": "a", "b.txt": "b"})

	c1, c2 := &dp.CountingObserver{}, &dp.CountingObserver{}
	multi := dp.NewMultiObserver(c1, nil, c2) // nil is skipped

	_, err := dp.New(dp.WithObserver(multi)).Prune(context.Background(), dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// Both counters must receive the same events.
	if c1.Total() != c2.Total() || c1.Total() == 0 {
		t.Errorf("MultiObserver: c1.Total=%d c2.Total=%d", c1.Total(), c2.Total())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Batcher integration
// ─────────────────────────────────────────────────────────────────────────────

// TestPrune_RecordingBatcher_OpKinds verifies that files use OpRemove and
// non-matching directories use OpRemoveAll.
func TestPrune_RecordingBatcher_OpKinds(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"keep.go":        "k",
		"delete.txt":     "d",      // OpRemove
		"vendor/dep.go":  "dep",    // OpRemoveAll (collapsed dir)
	})

	rb := &dirsync.RecordingBatcher{}
	_, err := dp.New(
		dp.WithIncludePatterns("keep.go"),
		dp.WithBatcher(func(_ dirsync.MergedView) (dirsync.Batcher, error) {
			return rb, nil
		}),
	).Prune(context.Background(), dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	ops := rb.Ops()
	opsByPath := make(map[string]dirsync.OpKind, len(ops))
	for _, op := range ops {
		opsByPath[op.RelPath] = op.Kind
	}

	if opsByPath["delete.txt"] != dirsync.OpRemove {
		t.Errorf("delete.txt: expected OpRemove, got %v", opsByPath["delete.txt"])
	}
	if opsByPath["vendor"] != dirsync.OpRemoveAll {
		t.Errorf("vendor: expected OpRemoveAll, got %v", opsByPath["vendor"])
	}
	if _, ok := opsByPath["keep.go"]; ok {
		t.Error("keep.go must not appear in ops (it's kept)")
	}
}

// TestPrune_FSBatcher_ActualDeletion verifies end-to-end with the real FS batcher.
func TestPrune_FSBatcher_ActualDeletion(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, map[string]string{
		"main.go":       "main",
		"util.go":       "util",
		"Dockerfile":    "docker",
		"scripts/":      "",
		"scripts/deploy.sh": "sh",
	})

	_, err := dp.New(
		dp.WithAllowWildcards(true),
		dp.WithIncludePatterns("*.go"),
	).Prune(context.Background(), dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	mustExist(t, filepath.Join(dir, "main.go"), "main.go")
	mustExist(t, filepath.Join(dir, "util.go"), "util.go")
	mustNotExist(t, filepath.Join(dir, "Dockerfile"), "Dockerfile deleted")
	mustNotExist(t, filepath.Join(dir, "scripts"), "scripts/ deleted")
}

// ─────────────────────────────────────────────────────────────────────────────
// Result tests
// ─────────────────────────────────────────────────────────────────────────────

func TestResult_Total_OK(t *testing.T) {
	r := dp.Result{Kept: 3, Deleted: 2, Collapsed: 1}
	if r.Total() != 6 {
		t.Errorf("Total: got %d, want 6", r.Total())
	}
	if !r.OK() {
		t.Error("Result with no errors should be OK")
	}

	r2 := dp.Result{SubmitErrors: 1}
	if r2.OK() {
		t.Error("Result with SubmitErrors should not be OK")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkPrune_LargeTree_AllDelete(b *testing.B) {
	const N = 500
	dir := b.TempDir()
	for i := 0; i < N; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o644)
	}

	b.ResetTimer()
	for range b.N {
		// Repopulate the directory for each run.
		for i := 0; i < N; i++ {
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o644)
		}
		// Prune with include "keep.go" → everything is deleted (no match).
		dp.New(dp.WithIncludePatterns("keep.go")).Prune(context.Background(), dir) //nolint:errcheck
	}
}

func BenchmarkPrune_RecordingBatcher_NoIO(b *testing.B) {
	const N = 1000
	dir := b.TempDir()
	for i := 0; i < N; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o644)
	}

	rb := &dirsync.RecordingBatcher{}
	opt := dp.WithBatcher(func(_ dirsync.MergedView) (dirsync.Batcher, error) {
		return rb, nil
	})

	b.ResetTimer()
	for range b.N {
		dp.New(dp.WithIncludePatterns("keep.go"), opt).Prune(context.Background(), dir) //nolint:errcheck
	}
}
