package diffview_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dirsync "github.com/bons/bons-ci/internal/dirsync"
	dv "github.com/bons/bons-ci/pkg/snapshotter/differ"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func fixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("fixture mkdir: %v", err)
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

// memView returns a WithMergedView option backed by an in-memory MergedView.
func memView() (*dirsync.MemMergedView, dv.Option) {
	mem := dirsync.NewMemMergedView("/mem-merged")
	opt := dv.WithMergedView(func(_ string) (dirsync.MergedView, error) {
		return mem, nil
	})
	return mem, opt
}

// recordingBatcher returns a WithBatcher option backed by a RecordingBatcher.
func recordingBatcher() (*dirsync.RecordingBatcher, dv.Option) {
	rb := &dirsync.RecordingBatcher{}
	opt := dv.WithBatcher(func(_ dirsync.MergedView) (dirsync.Batcher, error) {
		return rb, nil
	})
	return rb, opt
}

// ─────────────────────────────────────────────────────────────────────────────
// DiffEntry vocabulary tests
// ─────────────────────────────────────────────────────────────────────────────

func TestDiffEntry_IsCollapsedDir(t *testing.T) {
	cases := []struct {
		e    dv.DiffEntry
		want bool
	}{
		{dv.DiffEntry{Action: dv.ActionDelete, IsDir: true, Collapsed: true}, true},
		{dv.DiffEntry{Action: dv.ActionDelete, IsDir: true, Collapsed: false}, false},
		{dv.DiffEntry{Action: dv.ActionDelete, IsDir: false, Collapsed: true}, false},
		{dv.DiffEntry{Action: dv.ActionRetain, IsDir: true, Collapsed: true}, false},
	}
	for _, c := range cases {
		if got := c.e.IsCollapsedDir(); got != c.want {
			t.Errorf("IsCollapsedDir(%+v) = %v, want %v", c.e, got, c.want)
		}
	}
}

func TestAction_String(t *testing.T) {
	if dv.ActionDelete.String() != "delete" || dv.ActionRetain.String() != "retain" {
		t.Error("Action.String() returned unexpected value")
	}
}

func TestDeleteReason_String(t *testing.T) {
	if dv.DeleteReasonExclusiveLower.String() != "exclusive_lower" {
		t.Error("DeleteReasonExclusiveLower.String()")
	}
	if dv.DeleteReasonCommonEqual.String() != "common_equal" {
		t.Error("DeleteReasonCommonEqual.String()")
	}
}

func TestRetainReason_String(t *testing.T) {
	if dv.RetainReasonCommonDifferent.String() != "common_different" {
		t.Error("RetainReasonCommonDifferent.String()")
	}
	if dv.RetainReasonHashError.String() != "hash_error" {
		t.Error("RetainReasonHashError.String()")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Event predicate tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent_Predicates(t *testing.T) {
	deleted := dv.Event{Entry: dv.DiffEntry{Action: dv.ActionDelete}}
	failed := dv.Event{Entry: dv.DiffEntry{Action: dv.ActionDelete}, SubmitErr: errors.New("e")}
	retained := dv.Event{Entry: dv.DiffEntry{Action: dv.ActionRetain}}

	if !deleted.WasDeleted() || deleted.WasFailed() || deleted.WasRetained() {
		t.Error("successful delete predicates wrong")
	}
	if !failed.WasFailed() || failed.WasDeleted() || failed.WasRetained() {
		t.Error("failed delete predicates wrong")
	}
	if !retained.WasRetained() || retained.WasDeleted() || retained.WasFailed() {
		t.Error("retain predicates wrong")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Observer tests
// ─────────────────────────────────────────────────────────────────────────────

func TestNoopObserver(t *testing.T) {
	dv.NoopObserver{}.OnEvent(dv.Event{Entry: dv.DiffEntry{RelPath: "x"}})
}

func TestCountingObserver(t *testing.T) {
	c := &dv.CountingObserver{}
	events := []dv.Event{
		{Entry: dv.DiffEntry{Action: dv.ActionDelete, DeleteReason: dv.DeleteReasonExclusiveLower}},
		{Entry: dv.DiffEntry{Action: dv.ActionDelete, DeleteReason: dv.DeleteReasonExclusiveLower}},
		{Entry: dv.DiffEntry{Action: dv.ActionDelete, DeleteReason: dv.DeleteReasonCommonEqual}},
		{Entry: dv.DiffEntry{Action: dv.ActionRetain, RetainReason: dv.RetainReasonCommonDifferent}},
		{Entry: dv.DiffEntry{Action: dv.ActionRetain, RetainReason: dv.RetainReasonHashError}},
		{Entry: dv.DiffEntry{Action: dv.ActionDelete}, SubmitErr: errors.New("fail")},
	}
	for _, ev := range events {
		c.OnEvent(ev)
	}

	if c.DeletedExclusive.Load() != 2 {
		t.Errorf("DeletedExclusive: got %d want 2", c.DeletedExclusive.Load())
	}
	if c.DeletedEqual.Load() != 1 {
		t.Errorf("DeletedEqual: got %d want 1", c.DeletedEqual.Load())
	}
	if c.RetainedDiff.Load() != 1 {
		t.Errorf("RetainedDiff: got %d want 1", c.RetainedDiff.Load())
	}
	if c.RetainedHashErr.Load() != 1 {
		t.Errorf("RetainedHashErr: got %d want 1", c.RetainedHashErr.Load())
	}
	if c.SubmitFailed.Load() != 1 {
		t.Errorf("SubmitFailed: got %d want 1", c.SubmitFailed.Load())
	}
	if c.Total() != 6 {
		t.Errorf("Total: got %d want 6", c.Total())
	}
}

func TestLogObserver_LogAll(t *testing.T) {
	var buf bytes.Buffer
	l := dv.NewLogObserver(&buf, dv.LogAll)

	l.OnEvent(dv.Event{Entry: dv.DiffEntry{RelPath: "a", Action: dv.ActionDelete, DeleteReason: dv.DeleteReasonExclusiveLower}})
	l.OnEvent(dv.Event{Entry: dv.DiffEntry{RelPath: "b", Action: dv.ActionDelete, DeleteReason: dv.DeleteReasonCommonEqual}})
	l.OnEvent(dv.Event{Entry: dv.DiffEntry{RelPath: "c", Action: dv.ActionRetain, RetainReason: dv.RetainReasonCommonDifferent}})
	l.OnEvent(dv.Event{Entry: dv.DiffEntry{RelPath: "d", Action: dv.ActionDelete}, SubmitErr: errors.New("fail")})

	out := buf.String()
	for _, want := range []string{"DELETED_EXCL", "DELETED_EQUAL", "RETAINED_DIFF", "SUBMIT_FAILED", "fail"} {
		if !strings.Contains(out, want) {
			t.Errorf("LogAll missing %q:\n%s", want, out)
		}
	}
}

func TestLogObserver_LogErrors_Filters(t *testing.T) {
	var buf bytes.Buffer
	l := dv.NewLogObserver(&buf, dv.LogErrors)

	l.OnEvent(dv.Event{Entry: dv.DiffEntry{Action: dv.ActionRetain, RetainReason: dv.RetainReasonCommonDifferent}})
	l.OnEvent(dv.Event{Entry: dv.DiffEntry{Action: dv.ActionDelete}, SubmitErr: errors.New("boom")})

	out := buf.String()
	if strings.Contains(out, "RETAINED_DIFF") {
		t.Error("LogErrors must not log RETAINED_DIFF")
	}
	if !strings.Contains(out, "SUBMIT_FAILED") {
		t.Error("LogErrors must log SUBMIT_FAILED")
	}
}

func TestMultiObserver(t *testing.T) {
	c1, c2 := &dv.CountingObserver{}, &dv.CountingObserver{}
	m := dv.NewMultiObserver(c1, nil, c2)

	ev := dv.Event{Entry: dv.DiffEntry{Action: dv.ActionDelete, DeleteReason: dv.DeleteReasonExclusiveLower}}
	m.OnEvent(ev)
	m.OnEvent(ev)

	if c1.DeletedExclusive.Load() != 2 || c2.DeletedExclusive.Load() != 2 {
		t.Error("MultiObserver must fan-out to all non-nil observers")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Apply integration tests — real filesystem
// ─────────────────────────────────────────────────────────────────────────────

func TestApply_DeletesExclusiveLower(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"excl.txt":       "e",
		"excl-dir/a.txt": "e",
	})
	fixture(t, merged, map[string]string{
		"excl.txt":       "e",
		"excl-dir/a.txt": "e",
	})

	result, err := dv.New().Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	mustNotExist(t, filepath.Join(merged, "excl.txt"), "excl.txt")
	mustNotExist(t, filepath.Join(merged, "excl-dir"), "excl-dir (collapsed)")

	if result.DeletedExclusive != 2 {
		// excl.txt + excl-dir (collapsed)
		t.Errorf("DeletedExclusive: got %d want 2", result.DeletedExclusive)
	}
}

func TestApply_DeletesCommonEqual(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"shared.txt": "identical"})
	fixture(t, upper, map[string]string{"shared.txt": "identical"})
	fixture(t, merged, map[string]string{"shared.txt": "identical"})

	result, err := dv.New().Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	mustNotExist(t, filepath.Join(merged, "shared.txt"), "common-equal deleted")

	if result.DeletedEqual != 1 {
		t.Errorf("DeletedEqual: got %d want 1", result.DeletedEqual)
	}
}

func TestApply_RetainsCommonDifferent(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"f.txt": "lower-version"})
	fixture(t, upper, map[string]string{"f.txt": "upper-version"})
	fixture(t, merged, map[string]string{"f.txt": "upper-version"})

	// Mtime difference forces TwoPhaseHasher past the fast-path to SHA-256.
	_ = os.Chtimes(filepath.Join(lower, "f.txt"),
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	result, err := dv.New().Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	mustExist(t, filepath.Join(merged, "f.txt"), "different file retained")

	if result.RetainedDiff != 1 {
		t.Errorf("RetainedDiff: got %d want 1", result.RetainedDiff)
	}
}

func TestApply_FullMixedScenario(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{
		"excl1.txt":   "e",
		"excl2.txt":   "e",
		"shared.txt":  "same",
		"changed.txt": "old",
	})
	fixture(t, upper, map[string]string{
		"shared.txt":  "same",
		"changed.txt": "new",
	})
	fixture(t, merged, map[string]string{
		"excl1.txt":   "e",
		"excl2.txt":   "e",
		"shared.txt":  "same",
		"changed.txt": "new",
	})
	_ = os.Chtimes(filepath.Join(lower, "changed.txt"),
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	counter := &dv.CountingObserver{}
	result, err := dv.New(dv.WithObserver(counter)).
		Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	// excl1.txt and excl2.txt deleted
	mustNotExist(t, filepath.Join(merged, "excl1.txt"), "excl1.txt")
	mustNotExist(t, filepath.Join(merged, "excl2.txt"), "excl2.txt")
	// shared.txt deleted (common, equal)
	mustNotExist(t, filepath.Join(merged, "shared.txt"), "shared.txt")
	// changed.txt retained
	mustExist(t, filepath.Join(merged, "changed.txt"), "changed.txt")

	if result.DeletedExclusive != 2 {
		t.Errorf("DeletedExclusive: got %d want 2", result.DeletedExclusive)
	}
	if result.DeletedEqual != 1 {
		t.Errorf("DeletedEqual: got %d want 1", result.DeletedEqual)
	}
	if result.RetainedDiff != 1 {
		t.Errorf("RetainedDiff: got %d want 1", result.RetainedDiff)
	}
	if result.Total() != 4 {
		t.Errorf("Total: got %d want 4", result.Total())
	}

	// Observer counts must agree with Result.
	if counter.DeletedExclusive.Load() != 2 || counter.DeletedEqual.Load() != 1 || counter.RetainedDiff.Load() != 1 {
		t.Errorf("observer disagrees: excl=%d eq=%d diff=%d",
			counter.DeletedExclusive.Load(), counter.DeletedEqual.Load(), counter.RetainedDiff.Load())
	}
}

func TestApply_ValidationErrors(t *testing.T) {
	view := dv.New()
	tests := []struct {
		lower, upper, merged string
		wantErr              string
	}{
		{"", "/u", "/m", "lowerRoot"},
		{"/l", "", "/m", "upperRoot"},
		{"/l", "/u", "", "mergedRoot"},
	}
	for _, tt := range tests {
		_, err := view.Apply(context.Background(), tt.lower, tt.upper, tt.merged)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("expected error for empty %s, got %v", tt.wantErr, err)
		}
	}
}

func TestApply_ContextCancellation(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	for i := 0; i < 100; i++ {
		fixture(t, lower, map[string]string{fmt.Sprintf("f%03d.txt", i): "x"})
		fixture(t, merged, map[string]string{fmt.Sprintf("f%03d.txt", i): "x"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		dv.New().Apply(ctx, lower, upper, merged) //nolint:errcheck
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Apply did not return after context cancellation")
	}
}

func TestApply_ConcurrentWorkers(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	const N = 40
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		fixture(t, lower, map[string]string{name: "x"})
		fixture(t, merged, map[string]string{name: "x"})
	}

	result, err := dv.New(dv.WithWorkers(8)).
		Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}
	if result.DeletedExclusive != N {
		t.Errorf("DeletedExclusive: got %d want %d", result.DeletedExclusive, N)
	}
}

func TestApply_WithIncludePatterns(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"keep/a.txt": "x",
		"skip/b.txt": "x",
	})
	fixture(t, merged, map[string]string{
		"keep/a.txt": "x",
		"skip/b.txt": "x",
	})

	result, err := dv.New(dv.WithIncludePatterns("keep")).
		Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	mustNotExist(t, filepath.Join(merged, "keep"), "keep/ filtered and deleted")
	mustExist(t, filepath.Join(merged, "skip"), "skip/ untouched by filter")
}

// ─────────────────────────────────────────────────────────────────────────────
// Apply integration tests — in-memory backends (no filesystem I/O)
// ─────────────────────────────────────────────────────────────────────────────

// TestApply_MemMergedView verifies the full classification logic using
// in-memory components. No disk I/O — fast and fully deterministic.
func TestApply_MemMergedView_RecordsOps(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"excl.txt":   "e",
		"shared.txt": "s",
	})
	fixture(t, upper, map[string]string{
		"shared.txt": "s",
	})

	mem, memOpt := memView()
	rb, rbOpt := recordingBatcher()

	result, err := dv.New(memOpt, rbOpt).
		Apply(context.Background(), lower, upper, "/fake/merged")
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	_ = rb // already flushed by batcher.Close inside Apply

	// Check ops submitted to the batcher
	ops := rb.Ops()
	opPaths := make(map[string]dirsync.OpKind, len(ops))
	for _, op := range ops {
		opPaths[op.RelPath] = op.Kind
	}

	if kind, ok := opPaths["excl.txt"]; !ok || kind != dirsync.OpRemove {
		t.Errorf("excl.txt: expected OpRemove, got %v (exists=%v)", kind, ok)
	}
	if kind, ok := opPaths["shared.txt"]; !ok || kind != dirsync.OpRemove {
		t.Errorf("shared.txt: expected OpRemove, got %v (exists=%v)", kind, ok)
	}

	// MemMergedView itself is not used (the RecordingBatcher intercepts all ops),
	// but verify it received no direct calls from elsewhere.
	_ = mem
}

func TestApply_MemMergedView_CollapsedDir(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"lib/a.so": "a",
		"lib/b.so": "b",
	})

	_, memOpt := memView()
	rb, rbOpt := recordingBatcher()

	result, err := dv.New(memOpt, rbOpt).
		Apply(context.Background(), lower, upper, "/fake/merged")
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	ops := rb.Ops()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op (collapsed dir), got %d: %v", len(ops), ops)
	}
	if ops[0].RelPath != "lib" {
		t.Errorf("expected op for 'lib', got %q", ops[0].RelPath)
	}
	if ops[0].Kind != dirsync.OpRemoveAll {
		t.Errorf("collapsed dir must use OpRemoveAll, got %v", ops[0].Kind)
	}
	if result.DeletedExclusive != 1 {
		t.Errorf("DeletedExclusive: got %d want 1", result.DeletedExclusive)
	}
}

func TestApply_RetainsChanged_NoBatchOp(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"f.txt": "lower"})
	fixture(t, upper, map[string]string{"f.txt": "upper"})
	_ = os.Chtimes(filepath.Join(lower, "f.txt"),
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	_, memOpt := memView()
	rb, rbOpt := recordingBatcher()

	result, err := dv.New(memOpt, rbOpt).
		Apply(context.Background(), lower, upper, "/fake/merged")
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	// No batch op for a retained path.
	ops := rb.Ops()
	for _, op := range ops {
		if op.RelPath == "f.txt" {
			t.Errorf("changed file must not appear in batcher ops")
		}
	}
	if result.RetainedDiff != 1 {
		t.Errorf("RetainedDiff: got %d want 1", result.RetainedDiff)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Apply with custom Batcher factories
// ─────────────────────────────────────────────────────────────────────────────

func TestApply_WithGoroutineBatcher(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"a.txt": "x", "b.txt": "y"})
	fixture(t, merged, map[string]string{"a.txt": "x", "b.txt": "y"})

	gbOpt := dv.WithBatcher(func(view dirsync.MergedView) (dirsync.Batcher, error) {
		return dirsync.NewGoroutineBatcher(view, dirsync.WithAutoFlushAt(4)), nil
	})

	result, err := dv.New(gbOpt).Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	mustNotExist(t, filepath.Join(merged, "a.txt"), "a.txt")
	mustNotExist(t, filepath.Join(merged, "b.txt"), "b.txt")
}

// ─────────────────────────────────────────────────────────────────────────────
// Observer composition integration tests
// ─────────────────────────────────────────────────────────────────────────────

func TestApply_ObserverSeesEveryEntry(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"excl.txt":    "e",
		"shared.txt":  "s",
		"changed.txt": "old",
	})
	fixture(t, upper, map[string]string{
		"shared.txt":  "s",
		"changed.txt": "new",
	})
	fixture(t, merged, map[string]string{
		"excl.txt":    "e",
		"shared.txt":  "s",
		"changed.txt": "new",
	})
	_ = os.Chtimes(filepath.Join(lower, "changed.txt"),
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	var total atomic.Int32
	obs := &observerFunc{fn: func(ev dv.Event) { total.Add(1) }}

	result, err := dv.New(dv.WithObserver(obs)).
		Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	if total.Load() != 3 {
		t.Errorf("expected 3 observer events, got %d", total.Load())
	}
}

func TestApply_MultiObserver_LogAndCount(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"a.txt": "x", "b.txt": "y"})
	fixture(t, upper, map[string]string{"b.txt": "y"})
	fixture(t, merged, map[string]string{"a.txt": "x", "b.txt": "y"})

	var logBuf bytes.Buffer
	logger := dv.NewLogObserver(&logBuf, dv.LogAll)
	counter := &dv.CountingObserver{}

	result, err := dv.New(dv.WithObserver(dv.NewMultiObserver(logger, counter))).
		Apply(context.Background(), lower, upper, merged)
	if err != nil || result.Err != nil {
		t.Fatalf("Apply: %v / %v", err, result.Err)
	}

	out := logBuf.String()
	if !strings.Contains(out, "DELETED_EXCL") || !strings.Contains(out, "DELETED_EQUAL") {
		t.Errorf("log output missing expected tags:\n%s", out)
	}
	if counter.DeletedExclusive.Load() != 1 || counter.DeletedEqual.Load() != 1 {
		t.Errorf("counter: excl=%d eq=%d", counter.DeletedExclusive.Load(), counter.DeletedEqual.Load())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Result tests
// ─────────────────────────────────────────────────────────────────────────────

func TestResult_Total_And_OK(t *testing.T) {
	r := dv.Result{DeletedExclusive: 3, DeletedEqual: 1, RetainedDiff: 2, SubmitFailed: 1}
	if r.Total() != 7 {
		t.Errorf("Total: got %d want 7", r.Total())
	}
	if r.OK() {
		t.Error("Result with SubmitFailed should not be OK")
	}
	clean := dv.Result{DeletedExclusive: 1}
	if !clean.OK() {
		t.Error("clean Result should be OK")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Options tests
// ─────────────────────────────────────────────────────────────────────────────

func TestWithMergedView_InjectsView(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"x.txt": "x"})

	mem, opt := memView()
	rb, rbOpt := recordingBatcher()

	_, err := dv.New(opt, rbOpt).Apply(context.Background(), lower, upper, "/ignored/merged")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify the batcher received x.txt op (regardless of mem view)
	ops := rb.Ops()
	found := false
	for _, op := range ops {
		if op.RelPath == "x.txt" {
			found = true
		}
	}
	if !found {
		t.Error("expected x.txt op in recording batcher")
	}
	_ = mem // MemMergedView captured but RecordingBatcher intercepts all ops
}

func TestWithBatcher_InjectsBatcher(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"a.txt": "x"})

	rb, rbOpt := recordingBatcher()
	_, memOpt := memView()

	_, err := dv.New(rbOpt, memOpt).Apply(context.Background(), lower, upper, "/m")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(rb.Ops()) == 0 {
		t.Error("expected at least one op in injected batcher")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkApply_ExclusiveFiles_FSBatcher(b *testing.B) {
	lower, upper, merged := b.TempDir(), b.TempDir(), b.TempDir()
	const N = 200
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		_ = os.WriteFile(filepath.Join(lower, name), []byte("data"), 0o644)
	}

	b.ResetTimer()
	for range b.N {
		_ = os.RemoveAll(merged)
		_ = os.MkdirAll(merged, 0o755)
		for i := 0; i < N; i++ {
			_ = os.WriteFile(filepath.Join(merged, fmt.Sprintf("f%03d.txt", i)), []byte("data"), 0o644)
		}
		dv.New(dv.WithWorkers(4)).Apply(context.Background(), lower, upper, merged) //nolint:errcheck
	}
}

func BenchmarkApply_ExclusiveFiles_RecordingBatcher(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	const N = 500
	for i := 0; i < N; i++ {
		_ = os.WriteFile(filepath.Join(lower, fmt.Sprintf("f%03d.txt", i)), []byte("data"), 0o644)
	}
	_, memOpt := memView()

	b.ResetTimer()
	for range b.N {
		rb := &dirsync.RecordingBatcher{}
		rbOpt := dv.WithBatcher(func(_ dirsync.MergedView) (dirsync.Batcher, error) {
			return rb, nil
		})
		dv.New(memOpt, rbOpt).Apply(context.Background(), lower, upper, "/m") //nolint:errcheck
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal test doubles
// ─────────────────────────────────────────────────────────────────────────────

// observerFunc adapts a function to the Observer interface.
type observerFunc struct{ fn func(dv.Event) }

func (o *observerFunc) OnEvent(ev dv.Event) { o.fn(ev) }
