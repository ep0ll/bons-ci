package diffview_test

// diffview_test.go – black-box tests for the diffview package.
//
// Every test sets up three directories:
//   lower  – base layer (compared but never modified)
//   upper  – diff layer (compared but never modified)
//   merged – overlay union (the target of all deletions)
//
// The merged directory is populated with the union of lower + upper content
// before each test.  After Apply, tests verify what remains in merged.
//
// Test matrix:
//
//  ┌─ Delete from merged: lower-exclusive paths
//  │    TestApply_Exclusive_File       – orphan file deleted from merged
//  │    TestApply_Exclusive_Dir        – pruned subtree deleted from merged (O(1))
//  │
//  ├─ Delete from merged: common-and-equal paths
//  │    TestApply_Common_MetaEqual     – same mtime → MetaEqual → deleted from merged
//  │    TestApply_Common_HashEqual     – same content, diff mtime → HashEqual → deleted
//  │
//  ├─ Keep in merged: common-and-different paths
//  │    TestApply_Common_Different     – content differs → kept in merged
//  │
//  ├─ Mixed scenario
//  │    TestApply_Mixed                – all three cases in one tree
//  │
//  ├─ Edge cases
//  │    TestApply_EmptyLower           – nothing to delete
//  │    TestApply_EmptyUpper           – all lower-exclusive → all deleted from merged
//  │
//  ├─ DryRunDeleter
//  │    TestApply_DryRun               – no filesystem changes; records entries
//  │
//  ├─ Worker pool
//  │    TestApply_Workers              – multiple workers, race-detector input
//  │
//  ├─ Observer
//  │    TestApply_Observer_Counts      – CountingObserver matches Result
//  │    TestApply_Observer_Multi       – MultiObserver dispatches to both
//  │    TestApply_Observer_Log         – LogObserver writes expected tags
//  │
//  ├─ Context cancellation
//  │    TestApply_ContextCancelled     – cancel propagates; no hang
//  │
//  ├─ Validation
//  │    TestApply_InvalidRoots         – empty root paths return errors
//  │
//  └─ Error types
//       TestDeletionErrors             – *DeletionErrors wraps per-path failures
//       TestDeletionError_Unwrap       – Unwrap returns underlying error

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/snapshotter/differ"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// dirs creates a set of temporary test directories:
//
//	lower  – base layer (source; never modified by Apply)
//	upper  – diff layer (source; never modified by Apply)
//	merged – overlay union (target of all deletions)
func dirs(t *testing.T) (lower, upper, merged string) {
	t.Helper()
	lower = t.TempDir()
	upper = t.TempDir()
	merged = t.TempDir()
	return
}

// writeFile creates parent directories and writes content.
func writeFile(t *testing.T, root, content string, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// touchAt writes content and sets an explicit mtime.
func touchAt(t *testing.T, root, content string, mtime time.Time, parts ...string) string {
	t.Helper()
	p := writeFile(t, root, content, parts...)
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

// populateMerged copies a file from src to dst/relPath.
// This simulates the overlay union: merged contains the union of lower + upper.
func populateMerged(t *testing.T, merged, relPath, content string) string {
	t.Helper()
	return writeFile(t, merged, content, relPath)
}

// exists reports whether a path exists on disk.
func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return true
}

// applyOK runs Apply and fatals on a top-level (option/config) error.
// Deletion errors embedded in Result.Err are the test's responsibility.
func applyOK(t *testing.T, dv *diffview.DiffView, lower, upper, merged string) diffview.Result {
	t.Helper()
	res, err := dv.Apply(context.Background(), lower, upper, merged)
	if err != nil {
		t.Fatalf("Apply option error: %v", err)
	}
	return res
}

// ─── Delete: lower-exclusive paths ───────────────────────────────────────────

// TestApply_Exclusive_File: a file in lower but not upper is deleted from merged.
func TestApply_Exclusive_File(t *testing.T) {
	lower, upper, merged := dirs(t)

	writeFile(t, lower, "data", "orphan.txt")
	populateMerged(t, merged, "orphan.txt", "data") // merged starts with it

	dv := diffview.New()
	res := applyOK(t, dv, lower, upper, merged)

	// merged must no longer contain orphan.txt.
	if exists(t, filepath.Join(merged, "orphan.txt")) {
		t.Error("orphan.txt should have been deleted from merged")
	}
	if res.DeletedExclusive != 1 {
		t.Errorf("DeletedExclusive = %d, want 1", res.DeletedExclusive)
	}
	// lower must be untouched.
	if !exists(t, filepath.Join(lower, "orphan.txt")) {
		t.Error("lower/orphan.txt must not be touched")
	}
	if res.Err != nil {
		t.Errorf("unexpected error: %v", res.Err)
	}
}

// TestApply_Exclusive_Dir: an exclusive subtree in lower is deleted from
// merged via a single RemoveAll call (pruning DSA payoff).
func TestApply_Exclusive_Dir(t *testing.T) {
	lower, upper, merged := dirs(t)

	writeFile(t, lower, "x", "only", "deep", "a.txt")
	writeFile(t, lower, "x", "only", "deep", "b.txt")
	writeFile(t, lower, "x", "only", "deep", "sub", "c.txt")

	// merged has the full subtree.
	populateMerged(t, merged, "only/deep/a.txt", "x")
	populateMerged(t, merged, "only/deep/b.txt", "x")
	populateMerged(t, merged, "only/deep/sub/c.txt", "x")

	dv := diffview.New()
	res := applyOK(t, dv, lower, upper, merged)

	if exists(t, filepath.Join(merged, "only")) {
		t.Error("only/ subtree should have been deleted from merged")
	}
	// Pruning DSA: one pruned root emitted → DeletedExclusive == 1,
	// not 3 (one per file inside the subtree).
	if res.DeletedExclusive != 1 {
		t.Errorf("DeletedExclusive = %d, want 1 (pruned root, not per-file)", res.DeletedExclusive)
	}
	// lower subtree must be untouched.
	if !exists(t, filepath.Join(lower, "only")) {
		t.Error("lower/only/ must not be touched")
	}
}

// ─── Delete: common-and-equal paths ──────────────────────────────────────────

// TestApply_Common_MetaEqual: identical file (same mtime) in both lower and
// upper is deleted from merged (redundant in the effective diff).
func TestApply_Common_MetaEqual(t *testing.T) {
	lower, upper, merged := dirs(t)

	fixed := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	touchAt(t, lower, "body", fixed, "same.txt")
	touchAt(t, upper, "body", fixed, "same.txt")
	populateMerged(t, merged, "same.txt", "body")

	dv := diffview.New()
	res := applyOK(t, dv, lower, upper, merged)

	if exists(t, filepath.Join(merged, "same.txt")) {
		t.Error("same.txt: MetaEqual → merged copy must be deleted")
	}
	if res.DeletedEqual != 1 {
		t.Errorf("DeletedEqual = %d, want 1", res.DeletedEqual)
	}
	// Sources must be untouched.
	if !exists(t, filepath.Join(lower, "same.txt")) {
		t.Error("lower/same.txt must not be touched")
	}
	if !exists(t, filepath.Join(upper, "same.txt")) {
		t.Error("upper/same.txt must not be touched")
	}
}

// TestApply_Common_HashEqual: same content but different mtime triggers
// SHA-256 comparison → HashEqual → deleted from merged.
func TestApply_Common_HashEqual(t *testing.T) {
	lower, upper, merged := dirs(t)

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	touchAt(t, lower, "same content", t1, "file.txt")
	touchAt(t, upper, "same content", t2, "file.txt")
	populateMerged(t, merged, "file.txt", "same content")

	dv := diffview.New()
	res := applyOK(t, dv, lower, upper, merged)

	if exists(t, filepath.Join(merged, "file.txt")) {
		t.Error("file.txt: HashEqual → merged copy must be deleted")
	}
	if res.DeletedEqual != 1 {
		t.Errorf("DeletedEqual = %d, want 1", res.DeletedEqual)
	}
}

// ─── Keep: common-and-different paths ────────────────────────────────────────

// TestApply_Common_Different: content differs between lower and upper →
// path is the effective diff → preserved in merged.
func TestApply_Common_Different(t *testing.T) {
	lower, upper, merged := dirs(t)

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	touchAt(t, lower, "base version", t1, "mod.txt")
	touchAt(t, upper, "new version", t2, "mod.txt")
	populateMerged(t, merged, "mod.txt", "new version") // merged has upper's version

	dv := diffview.New()
	res := applyOK(t, dv, lower, upper, merged)

	if !exists(t, filepath.Join(merged, "mod.txt")) {
		t.Error("mod.txt: content differs → merged copy must be kept (it is the effective diff)")
	}
	if res.RetainedDiff != 1 {
		t.Errorf("RetainedDiff = %d, want 1", res.RetainedDiff)
	}
	if res.DeletedEqual != 0 || res.DeletedExclusive != 0 {
		t.Errorf("no deletions expected; got excl=%d equal=%d",
			res.DeletedExclusive, res.DeletedEqual)
	}
}

// ─── Mixed scenario ───────────────────────────────────────────────────────────

// TestApply_Mixed: all three cases in a single tree.
func TestApply_Mixed(t *testing.T) {
	lower, upper, merged := dirs(t)

	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	later := base.Add(time.Second)

	// A: lower-exclusive → delete from merged.
	writeFile(t, lower, "data", "excl.txt")
	populateMerged(t, merged, "excl.txt", "data")

	// B: common + MetaEqual → delete from merged.
	touchAt(t, lower, "same", base, "equal.txt")
	touchAt(t, upper, "same", base, "equal.txt")
	populateMerged(t, merged, "equal.txt", "same")

	// C: common + different content → keep in merged.
	touchAt(t, lower, "lower body", base, "diff.txt")
	touchAt(t, upper, "upper body", later, "diff.txt")
	populateMerged(t, merged, "diff.txt", "upper body")

	// D: common + same content, different mtime (HashEqual) → delete.
	touchAt(t, lower, "body", base, "hash_eq.txt")
	touchAt(t, upper, "body", later, "hash_eq.txt")
	populateMerged(t, merged, "hash_eq.txt", "body")

	dv := diffview.New(diffview.WithWorkers(2))
	res := applyOK(t, dv, lower, upper, merged)

	// Filesystem assertions on merged.
	if exists(t, filepath.Join(merged, "excl.txt")) {
		t.Error("merged/excl.txt should be deleted (lower-exclusive)")
	}
	if exists(t, filepath.Join(merged, "equal.txt")) {
		t.Error("merged/equal.txt should be deleted (MetaEqual)")
	}
	if !exists(t, filepath.Join(merged, "diff.txt")) {
		t.Error("merged/diff.txt should be kept (different content)")
	}
	if exists(t, filepath.Join(merged, "hash_eq.txt")) {
		t.Error("merged/hash_eq.txt should be deleted (HashEqual)")
	}

	// Sources must be completely untouched.
	for _, path := range []string{
		filepath.Join(lower, "excl.txt"),
		filepath.Join(lower, "equal.txt"),
		filepath.Join(lower, "diff.txt"),
		filepath.Join(lower, "hash_eq.txt"),
		filepath.Join(upper, "equal.txt"),
		filepath.Join(upper, "diff.txt"),
		filepath.Join(upper, "hash_eq.txt"),
	} {
		if !exists(t, path) {
			t.Errorf("source must not be touched: %s", path)
		}
	}

	// Result count assertions.
	if res.DeletedExclusive != 1 {
		t.Errorf("DeletedExclusive = %d, want 1", res.DeletedExclusive)
	}
	if res.DeletedEqual != 2 {
		t.Errorf("DeletedEqual = %d, want 2 (equal.txt + hash_eq.txt)", res.DeletedEqual)
	}
	if res.RetainedDiff != 1 {
		t.Errorf("RetainedDiff = %d, want 1", res.RetainedDiff)
	}
	if res.Err != nil {
		t.Errorf("unexpected error: %v", res.Err)
	}
}

// ─── Edge cases ───────────────────────────────────────────────────────────────

func TestApply_EmptyLower(t *testing.T) {
	lower, upper, merged := dirs(t)
	writeFile(t, upper, "upper only", "upper.txt")
	populateMerged(t, merged, "upper.txt", "upper only")

	res := applyOK(t, diffview.New(), lower, upper, merged)

	// Nothing in lower → nothing to delete from merged.
	if res.DeletedExclusive != 0 || res.DeletedEqual != 0 {
		t.Errorf("empty lower: want 0 deletions; got excl=%d equal=%d",
			res.DeletedExclusive, res.DeletedEqual)
	}
	if !exists(t, filepath.Join(merged, "upper.txt")) {
		t.Error("merged/upper.txt should remain (only in upper)")
	}
}

func TestApply_EmptyUpper(t *testing.T) {
	lower, upper, merged := dirs(t)

	writeFile(t, lower, "a", "a.txt")
	writeFile(t, lower, "b", "b.txt")
	populateMerged(t, merged, "a.txt", "a")
	populateMerged(t, merged, "b.txt", "b")

	res := applyOK(t, diffview.New(), lower, upper, merged)

	for _, f := range []string{"a.txt", "b.txt"} {
		if exists(t, filepath.Join(merged, f)) {
			t.Errorf("merged/%s should be deleted (lower-exclusive, upper is empty)", f)
		}
		if !exists(t, filepath.Join(lower, f)) {
			t.Errorf("lower/%s must not be touched", f)
		}
	}
	if res.DeletedExclusive != 2 {
		t.Errorf("DeletedExclusive = %d, want 2", res.DeletedExclusive)
	}
}

// ─── DryRunDeleter ────────────────────────────────────────────────────────────

// TestApply_DryRun: DryRunDeleter records targets without touching merged.
func TestApply_DryRun(t *testing.T) {
	lower, upper, merged := dirs(t)

	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	writeFile(t, lower, "data", "excl.txt")
	touchAt(t, lower, "same", fixed, "eq.txt")
	touchAt(t, upper, "same", fixed, "eq.txt")

	populateMerged(t, merged, "excl.txt", "data")
	populateMerged(t, merged, "eq.txt", "same")

	dry := diffview.NewDryRunDeleter()
	dv := diffview.New(diffview.WithDeleter(dry))
	res := applyOK(t, dv, lower, upper, merged)

	// merged must be completely untouched.
	if !exists(t, filepath.Join(merged, "excl.txt")) {
		t.Error("dry-run: merged/excl.txt must not be deleted")
	}
	if !exists(t, filepath.Join(merged, "eq.txt")) {
		t.Error("dry-run: merged/eq.txt must not be deleted")
	}

	// Recorded entries must reflect what would have been deleted.
	entries := dry.Entries()
	if len(entries) != 2 {
		t.Errorf("DryRunDeleter recorded %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Action != diffview.ActionDelete {
			t.Errorf("dry-run entry %q: Action = %v, want ActionDelete", e.RelPath, e.Action)
		}
	}

	// Result counts still reflect what WOULD have happened.
	if res.DeletedExclusive != 1 {
		t.Errorf("dry-run DeletedExclusive = %d, want 1", res.DeletedExclusive)
	}
	if res.DeletedEqual != 1 {
		t.Errorf("dry-run DeletedEqual = %d, want 1", res.DeletedEqual)
	}
	if res.Err != nil {
		t.Errorf("dry-run: unexpected error: %v", res.Err)
	}
}

// ─── Worker pool ──────────────────────────────────────────────────────────────

// TestApply_Workers: many exclusive files with 4 workers; race-detector input.
func TestApply_Workers(t *testing.T) {
	const n = 50
	lower, upper, merged := dirs(t)

	for i := 0; i < n; i++ {
		name := fmt.Sprintf("excl_%03d.txt", i)
		writeFile(t, lower, "data", name)
		populateMerged(t, merged, name, "data")
	}

	obs := &diffview.CountingObserver{}
	dv := diffview.New(
		diffview.WithWorkers(4),
		diffview.WithObserver(obs),
	)
	res := applyOK(t, dv, lower, upper, merged)

	if res.DeletedExclusive != n {
		t.Errorf("DeletedExclusive = %d, want %d", res.DeletedExclusive, n)
	}
	if got := obs.DeletedExclusive.Load(); got != int64(n) {
		t.Errorf("observer counted %d, want %d", got, n)
	}
	// Verify merged is empty (all files deleted).
	entries, _ := os.ReadDir(merged)
	if len(entries) != 0 {
		t.Errorf("merged should be empty after all-exclusive run; has %d entries", len(entries))
	}
	if res.Err != nil {
		t.Errorf("unexpected error: %v", res.Err)
	}
}

// ─── Observer ─────────────────────────────────────────────────────────────────

// TestApply_Observer_Counts: CountingObserver matches Result fields exactly.
func TestApply_Observer_Counts(t *testing.T) {
	lower, upper, merged := dirs(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	later := base.Add(time.Second)

	writeFile(t, lower, "x", "excl.txt")
	populateMerged(t, merged, "excl.txt", "x")

	touchAt(t, lower, "same", base, "eq.txt")
	touchAt(t, upper, "same", base, "eq.txt")
	populateMerged(t, merged, "eq.txt", "same")

	touchAt(t, lower, "lo", base, "diff.txt")
	touchAt(t, upper, "up", later, "diff.txt")
	populateMerged(t, merged, "diff.txt", "up")

	obs := &diffview.CountingObserver{}
	dv := diffview.New(diffview.WithObserver(obs))
	res := applyOK(t, dv, lower, upper, merged)

	check := func(name string, got int64, want int) {
		t.Helper()
		if got != int64(want) {
			t.Errorf("%s: observer=%d, result=%d", name, got, want)
		}
	}
	check("DeletedExclusive", obs.DeletedExclusive.Load(), res.DeletedExclusive)
	check("DeletedEqual", obs.DeletedEqual.Load(), res.DeletedEqual)
	check("RetainedDiff", obs.RetainedDiff.Load(), res.RetainedDiff)
}

// TestApply_Observer_Multi: MultiObserver dispatches to both observers.
func TestApply_Observer_Multi(t *testing.T) {
	lower, upper, merged := dirs(t)

	writeFile(t, lower, "x", "file.txt")
	populateMerged(t, merged, "file.txt", "x")

	obs1 := &diffview.CountingObserver{}
	obs2 := &diffview.CountingObserver{}
	dv := diffview.New(diffview.WithObserver(diffview.NewMultiObserver(obs1, obs2)))
	applyOK(t, dv, lower, upper, merged)

	if obs1.DeletedExclusive.Load() != 1 {
		t.Error("obs1: expected 1 DeletedExclusive")
	}
	if obs2.DeletedExclusive.Load() != 1 {
		t.Error("obs2: expected 1 DeletedExclusive")
	}
}

// TestApply_Observer_Log: LogObserver writes expected tag for deleted entries.
func TestApply_Observer_Log(t *testing.T) {
	lower, upper, merged := dirs(t)

	writeFile(t, lower, "x", "excl.txt")
	populateMerged(t, merged, "excl.txt", "x")

	var buf bytes.Buffer
	dv := diffview.New(diffview.WithObserver(diffview.NewLogObserver(&buf, diffview.LogAll)))
	applyOK(t, dv, lower, upper, merged)

	out := buf.String()
	if !strings.Contains(out, "excl.txt") {
		t.Errorf("LogObserver output missing path; got:\n%s", out)
	}
	if !strings.Contains(out, "DELETED_EXCL") {
		t.Errorf("LogObserver output missing tag; got:\n%s", out)
	}
}

// ─── Context cancellation ─────────────────────────────────────────────────────

func TestApply_ContextCancelled(t *testing.T) {
	lower, upper, merged := dirs(t)

	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		writeFile(t, lower, "x", name)
		populateMerged(t, merged, name, "x")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		dv := diffview.New()
		_, _ = dv.Apply(ctx, lower, upper, merged)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Apply did not return within 5 seconds after ctx cancel")
	}
}

// ─── Validation ───────────────────────────────────────────────────────────────

func TestApply_InvalidRoots(t *testing.T) {
	dv := diffview.New()
	ctx := context.Background()
	tmp := t.TempDir()

	cases := []struct {
		name         string
		lower, upper, merged string
	}{
		{"empty lower", "", tmp, tmp},
		{"empty upper", tmp, "", tmp},
		{"empty merged", tmp, tmp, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dv.Apply(ctx, tc.lower, tc.upper, tc.merged)
			if err == nil {
				t.Errorf("%s: expected validation error, got nil", tc.name)
			}
		})
	}
}

// ─── Error types ──────────────────────────────────────────────────────────────

// TestDeletionErrors: a locked directory causes RemoveAll to fail; the error
// is captured in Result.Err as *DeletionErrors.
// Critically, the successful deletions are NOT counted in DeletedExclusive.
func TestDeletionErrors(t *testing.T) {
	lower, upper, merged := dirs(t)

	// "locked/" — create in lower and merged, then lock merged/locked.
	writeFile(t, lower, "x", "locked", "file.txt")
	populateMerged(t, merged, "locked/file.txt", "x")
	if err := os.Chmod(filepath.Join(merged, "locked"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(merged, "locked"), 0o755) })

	dv := diffview.New()
	res, err := dv.Apply(context.Background(), lower, upper, merged)
	if err != nil {
		t.Fatalf("Apply option error: %v", err)
	}
	if res.Err == nil {
		t.Skip("deletion unexpectedly succeeded (running as root?)")
	}

	var dErr *diffview.DeletionErrors
	if !errors.As(res.Err, &dErr) {
		t.Errorf("expected *DeletionErrors, got %T: %v", res.Err, res.Err)
	}
	if len(dErr.Errors) == 0 {
		t.Error("DeletionErrors.Errors must not be empty")
	}
	// Failed deletions must NOT inflate DeletedExclusive.
	if res.DeletedExclusive != 0 {
		t.Errorf("failed deletion should not count; DeletedExclusive = %d, want 0",
			res.DeletedExclusive)
	}
}

func TestDeletionError_Unwrap(t *testing.T) {
	inner := os.ErrPermission
	de := diffview.DeletionError{RelPath: "x", MergedAbs: "/x", Err: inner}
	if !errors.Is(de, os.ErrPermission) {
		t.Error("DeletionError.Unwrap() must expose the inner error via errors.Is")
	}
}
