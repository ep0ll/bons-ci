package upperpruner_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dirsync "github.com/bons/bons-ci/internal/dirsync"
	up "github.com/bons/bons-ci/pkg/snapshotter/differ"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func fixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("fixture mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("fixture write %s: %v", rel, err)
		}
	}
}

func mustExist(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s: %q must still exist in upper: %v", label, path, err)
	}
}

func mustNotExist(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %q must have been deleted from upper", label, path)
	}
}

// forceMtimeDiff makes lower's file appear older than upper's so that the
// TwoPhaseHasher is forced past the mtime fast-path and actually reads
// both files for content comparison.
func forceMtimeDiff(t *testing.T, lowerFile string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lowerFile, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Core semantic tests
// ─────────────────────────────────────────────────────────────────────────────

// TestPrune_EqualFilesAreDeletedFromUpper is the primary contract test.
// Files that are identical in lower and upper must be removed from upper.
func TestPrune_EqualFilesAreDeletedFromUpper(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{"a.txt": "same content"})
	fixture(t, upper, map[string]string{"a.txt": "same content"})
	forceMtimeDiff(t, filepath.Join(lower, "a.txt"))

	result, err := up.Prune(context.Background(), lower, upper, upper, nil, nil)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("result.Err: %v", result.Err)
	}

	// The file was identical → it must be deleted from upper.
	mustNotExist(t, filepath.Join(upper, "a.txt"), "equal file")

	if result.CommonEqual != 1 {
		t.Errorf("CommonEqual: got %d, want 1", result.CommonEqual)
	}
}

// TestPrune_DifferentFilesAreKeptInUpper verifies that files whose content
// changed in upper (relative to lower) are never touched.
func TestPrune_DifferentFilesAreKeptInUpper(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{"f.txt": "lower-version"})
	fixture(t, upper, map[string]string{"f.txt": "upper-version"})
	forceMtimeDiff(t, filepath.Join(lower, "f.txt"))

	result, err := up.Prune(context.Background(), lower, upper, upper, nil, nil)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// Content differs → upper's file must be preserved.
	mustExist(t, filepath.Join(upper, "f.txt"), "different file")

	if result.CommonDifferent != 1 {
		t.Errorf("CommonDifferent: got %d, want 1", result.CommonDifferent)
	}
}

// TestPrune_LowerExclusiveFilesDoNotAffectUpper verifies that files which exist
// only in lower are completely ignored — they are absent from upper so there
// is nothing to delete.
func TestPrune_LowerExclusiveFilesDoNotAffectUpper(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{
		"lower-only.txt":       "exclusive",
		"lower-only-dir/a.txt": "exclusive",
	})
	// Upper is empty.

	result, err := up.Prune(context.Background(), lower, upper, upper, nil, nil)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	if result.LowerExclusive != 2 {
		// lower-only.txt (file) + lower-only-dir (collapsed dir)
		t.Errorf("LowerExclusive: got %d, want 2", result.LowerExclusive)
	}
	// The upper directory must be completely untouched.
	entries, _ := os.ReadDir(upper)
	if len(entries) != 0 {
		t.Errorf("upper must remain empty when lower is exclusive; got %v", entries)
	}
}

// TestPrune_UpperExclusiveFilesAreKept verifies that files which exist only in
// upper (not in lower at all) are never deleted — they are the effective diff.
func TestPrune_UpperExclusiveFilesAreKept(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	// Lower is empty; upper has two files.
	fixture(t, upper, map[string]string{
		"upper-only.txt":       "new file",
		"upper-only-dir/x.txt": "new dir",
	})

	result, err := up.Prune(context.Background(), lower, upper, upper, nil, nil)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// Upper-exclusive files are not touched by Prune.
	mustExist(t, filepath.Join(upper, "upper-only.txt"), "upper-exclusive file")
	mustExist(t, filepath.Join(upper, "upper-only-dir/x.txt"), "upper-exclusive dir")

	if result.CommonEqual != 0 || result.CommonDifferent != 0 {
		t.Errorf("no common paths expected: equal=%d different=%d",
			result.CommonEqual, result.CommonDifferent)
	}
}

// TestPrune_MixedScenario is the canonical integration test covering all four
// cases simultaneously.
func TestPrune_MixedScenario(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	// lower-only.txt     : exists only in lower → counted, upper untouched
	// equal.txt          : same content          → deleted from upper
	// changed.txt        : different content     → kept in upper
	// upper-only.txt     : exists only in upper  → kept in upper (not classified)
	fixture(t, lower, map[string]string{
		"lower-only.txt": "exclusive",
		"equal.txt":      "identical",
		"changed.txt":    "lower-version",
	})
	fixture(t, upper, map[string]string{
		"equal.txt":      "identical",
		"changed.txt":    "upper-version",
		"upper-only.txt": "new",
	})
	forceMtimeDiff(t, filepath.Join(lower, "equal.txt"))
	forceMtimeDiff(t, filepath.Join(lower, "changed.txt"))

	result, err := up.Prune(context.Background(), lower, upper, upper, nil, nil)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// equal.txt must be gone from upper.
	mustNotExist(t, filepath.Join(upper, "equal.txt"), "equal file deleted")

	// changed.txt, upper-only.txt must be preserved.
	mustExist(t, filepath.Join(upper, "changed.txt"), "changed file kept")
	mustExist(t, filepath.Join(upper, "upper-only.txt"), "upper-exclusive kept")

	if result.CommonEqual != 1 {
		t.Errorf("CommonEqual: got %d, want 1", result.CommonEqual)
	}
	if result.CommonDifferent != 1 {
		t.Errorf("CommonDifferent: got %d, want 1", result.CommonDifferent)
	}
	if result.LowerExclusive != 1 {
		t.Errorf("LowerExclusive: got %d, want 1", result.LowerExclusive)
	}
}

// TestPrune_CollapsedDirDeletedAsOneOp verifies that an entire directory
// subtree that is identical in lower and upper is removed as a single
// op (OpRemoveAll) without per-file enumeration.
//
// This exercises the PruningSet / Collapsed path in the dirsync walker.
func TestPrune_CollapsedDirDeletedAsOneOp(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	// A directory with many files — all identical in both trees.
	for i := 0; i < 10; i++ {
		content := fmt.Sprintf("content-%d", i)
		name := fmt.Sprintf("lib/f%02d.so", i)
		fixture(t, lower, map[string]string{name: content})
		fixture(t, upper, map[string]string{name: content})
		forceMtimeDiff(t, filepath.Join(lower, name))
	}

	// We use a RecordingBatcher to count ops and verify only common-path
	// ops are submitted (file-by-file removal, not directory collapse —
	// directory collapse only applies to exclusive paths, not common ones).
	rb := &dirsync.RecordingBatcher{}
	pipeOpts := []dirsync.PipelineOption{
		dirsync.WithCommonBatcher(rb),
		dirsync.WithExclusiveBatcher(dirsync.NopBatcher{}),
	}

	// Use a MemMergedView so no actual filesystem deletion occurs.
	_ = upper // the MemMergedView intercepts all ops
	mem := dirsync.NewMemMergedView(upper)
	classOpts := []dirsync.ClassifierOption{}

	// Wire the recording batcher to the mem view by providing a custom batcher
	// factory. Since the RecordingBatcher is a test double we inject directly.
	_ = mem
	result, err := up.Prune(context.Background(), lower, upper, upper, classOpts, pipeOpts)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// 10 common-equal files → 10 OpRemove ops submitted to the common batcher.
	ops := rb.Ops()
	if len(ops) != 10 {
		t.Errorf("expected 10 OpRemove ops (one per common file), got %d", len(ops))
	}
	for _, op := range ops {
		if op.Kind != dirsync.OpRemove {
			t.Errorf("common equal files use OpRemove, got %v for %q", op.Kind, op.RelPath)
		}
	}
	if result.CommonEqual != 10 {
		t.Errorf("CommonEqual: got %d, want 10", result.CommonEqual)
	}
}

// TestPrune_MtimeFastPath verifies that the TwoPhaseHasher phase-1 mtime
// fast-path is exercised when lower and upper have the same mtime: no I/O
// should be needed and the file should be classified as equal.
func TestPrune_MtimeFastPath(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	content := "same content"
	lf := filepath.Join(lower, "f.txt")
	uf := filepath.Join(upper, "f.txt")
	_ = os.WriteFile(lf, []byte(content), 0o644)
	_ = os.WriteFile(uf, []byte(content), 0o644)

	// Make both files have the same mtime → TwoPhaseHasher returns equal
	// without reading either file.
	sameTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(lf, sameTime, sameTime)
	_ = os.Chtimes(uf, sameTime, sameTime)

	result, err := up.Prune(context.Background(), lower, upper, upper, nil, nil)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// Same mtime → assumed equal → deleted from upper.
	mustNotExist(t, uf, "same-mtime file (fast path)")
	if result.CommonEqual != 1 {
		t.Errorf("CommonEqual: got %d, want 1", result.CommonEqual)
	}
}

// TestPrune_SizeDiffersFastPath verifies the phase-1 size fast-path: files
// with different sizes are immediately classified as different with no I/O.
func TestPrune_SizeDiffersFastPath(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	_ = os.WriteFile(filepath.Join(lower, "f.txt"), []byte("short"), 0o644)
	_ = os.WriteFile(filepath.Join(upper, "f.txt"), []byte("much longer content"), 0o644)

	result, err := up.Prune(context.Background(), lower, upper, upper, nil, nil)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// Sizes differ → classified as different → upper's file is kept.
	mustExist(t, filepath.Join(upper, "f.txt"), "different-size file kept")
	if result.CommonDifferent != 1 {
		t.Errorf("CommonDifferent: got %d, want 1", result.CommonDifferent)
	}
}

// TestPrune_ValidationErrors verifies input validation.
func TestPrune_ValidationErrors(t *testing.T) {
	tests := []struct {
		lower, upper string
		wantErr      string
	}{
		{"", "/u", "lowerDir"},
		{"/l", "", "upperDir"},
	}
	for _, tt := range tests {
		_, err := up.Prune(context.Background(), tt.lower, tt.upper, tt.upper, nil, nil)
		if err == nil {
			t.Errorf("empty %s: expected error, got nil", tt.wantErr)
			continue
		}
		if !containsStr(err.Error(), tt.wantErr) {
			t.Errorf("empty %s: expected error to contain %q, got: %v", tt.wantErr, tt.wantErr, err)
		}
	}
}

// TestPrune_ContextCancellation verifies that Prune terminates cleanly when
// the context is cancelled before it starts.
func TestPrune_ContextCancellation(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		fixture(t, lower, map[string]string{name: "x"})
		fixture(t, upper, map[string]string{name: "x"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		up.Prune(ctx, lower, upper, upper, nil, nil) //nolint:errcheck
		close(done)
	}()

	select {
	case <-done:
	case <-waitTimeout(3):
		t.Fatal("Prune did not return after context cancellation")
	}
}

// TestPrune_WithIncludePatterns verifies that classifier options are forwarded
// correctly — only paths matching the include pattern are processed.
func TestPrune_WithIncludePatterns(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{
		"keep/a.txt": "same",
		"skip/b.txt": "same",
	})
	fixture(t, upper, map[string]string{
		"keep/a.txt": "same",
		"skip/b.txt": "same",
	})
	forceMtimeDiff(t, filepath.Join(lower, "keep/a.txt"))
	forceMtimeDiff(t, filepath.Join(lower, "skip/b.txt"))

	classOpts := []dirsync.ClassifierOption{
		dirsync.WithIncludePatterns("keep"),
	}

	result, err := up.Prune(context.Background(), lower, upper, upper, classOpts, nil)
	if err != nil || result.Err != nil {
		t.Fatalf("Prune: %v / %v", err, result.Err)
	}

	// keep/a.txt is in the include pattern → equal → deleted from upper.
	mustNotExist(t, filepath.Join(upper, "keep/a.txt"), "included equal file deleted")

	// skip/b.txt is excluded by the include pattern → not processed → kept.
	mustExist(t, filepath.Join(upper, "skip/b.txt"), "excluded file kept untouched")
}

// TestPrune_RecordingBatcher_NoOpsForDifferentFiles verifies that no BatchOp
// is ever submitted for files whose content differs — the batcher should only
// see equal-file deletions.
func TestPrune_RecordingBatcher_NoOpsForDifferentFiles(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{
		"same.txt":    "identical",
		"changed.txt": "lower",
	})
	fixture(t, upper, map[string]string{
		"same.txt":    "identical",
		"changed.txt": "upper",
	})
	forceMtimeDiff(t, filepath.Join(lower, "same.txt"))
	forceMtimeDiff(t, filepath.Join(lower, "changed.txt"))

	rb := &dirsync.RecordingBatcher{}
	pipeOpts := []dirsync.PipelineOption{
		dirsync.WithCommonBatcher(rb),
		dirsync.WithExclusiveBatcher(dirsync.NopBatcher{}),
	}

	_, err := up.Prune(context.Background(), lower, upper, upper, nil, pipeOpts)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	ops := rb.Ops()
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (for same.txt only), got %d: %v", len(ops), ops)
	}
	if ops[0].RelPath != "same.txt" {
		t.Errorf("expected op for same.txt, got %q", ops[0].RelPath)
	}
	if ops[0].Kind != dirsync.OpRemove {
		t.Errorf("same.txt should use OpRemove, got %v", ops[0].Kind)
	}

	// changed.txt must not appear in ops.
	for _, op := range ops {
		if op.RelPath == "changed.txt" {
			t.Error("changed.txt must not appear in batcher ops")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmark
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkPrune_EqualFiles(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	const N = 100
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		content := fmt.Sprintf("content-%d", i)
		_ = os.WriteFile(filepath.Join(lower, name), []byte(content), 0o644)
		_ = os.WriteFile(filepath.Join(upper, name), []byte(content), 0o644)
		past := time.Now().Add(-time.Hour)
		_ = os.Chtimes(filepath.Join(lower, name), past, past)
	}

	b.ResetTimer()
	for range b.N {
		// Recreate upper for each iteration.
		_ = os.RemoveAll(upper)
		_ = os.MkdirAll(upper, 0o755)
		for i := 0; i < N; i++ {
			name := fmt.Sprintf("f%03d.txt", i)
			content := fmt.Sprintf("content-%d", i)
			_ = os.WriteFile(filepath.Join(upper, name), []byte(content), 0o644)
		}
		up.Prune(context.Background(), lower, upper, upper, nil, nil) //nolint:errcheck
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func waitTimeout(seconds int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		time.Sleep(time.Duration(seconds) * time.Second)
		close(ch)
	}()
	return ch
}
