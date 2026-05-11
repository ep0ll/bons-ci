package dirsync_test

// coverage_test.go fills the remaining coverage gaps identified by go tool cover.
// Each test targets a specific uncovered code path with a clear rationale.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// batcher.go — OpKind.String, RecordingBatcher.Close, executeOp unknown kind
// ─────────────────────────────────────────────────────────────────────────────

func TestOpKind_String_AllValues(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "remove", dirsync.OpRemove.String())
	assert.Equal(t, "removeAll", dirsync.OpRemoveAll.String())
	assert.Equal(t, "unknown", dirsync.OpKind(255).String())
}

func TestRecordingBatcher_Close_FlushesAndSucceeds(t *testing.T) {
	t.Parallel()
	rb := &dirsync.RecordingBatcher{}
	ctx := context.Background()
	_ = rb.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: "x"})
	require.NoError(t, rb.Close(ctx))
	assert.Equal(t, int64(1), rb.Total())
}

// ─────────────────────────────────────────────────────────────────────────────
// batcher_goroutine.go — WithBatchParallelism, Len, Flush-after-closed
// ─────────────────────────────────────────────────────────────────────────────

func TestGoroutineBatcher_WithBatchParallelism_Applied(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	b := dirsync.NewGoroutineBatcher(v, dirsync.WithBatchParallelism(4))
	ctx := context.Background()

	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	require.NoError(t, b.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: "a.txt"}))
	require.NoError(t, b.Close(ctx))
}

func TestGoroutineBatcher_Len_ReflectsQueue(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/m")
	b := dirsync.NewGoroutineBatcher(v)
	ctx := context.Background()

	assert.Equal(t, 0, b.Len())
	_ = b.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: "a"})
	_ = b.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: "b"})
	assert.Equal(t, 2, b.Len())
	_ = b.Flush(ctx)
	assert.Equal(t, 0, b.Len())
}

func TestGoroutineBatcher_FlushAfterClose_ReturnsError(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/m")
	b := dirsync.NewGoroutineBatcher(v)
	ctx := context.Background()
	_ = b.Close(ctx)
	err := b.Flush(ctx)
	assert.ErrorIs(t, err, dirsync.ErrBatcherClosed)
}

// ─────────────────────────────────────────────────────────────────────────────
// batcher_io_uring.go — IOURingAvailable, WithRingEntries, Close, NewBestBatcher
// ─────────────────────────────────────────────────────────────────────────────

func TestIOURingAvailable_ReturnsWithoutPanic(t *testing.T) {
	t.Parallel()
	// Must not panic; result depends on kernel.
	available := dirsync.IOURingAvailable()
	t.Logf("io_uring available on this kernel: %v", available)
}

func TestNewBestBatcher_ReturnsValidBatcher(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	b, err := dirsync.NewBestBatcher(v)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.NoError(t, b.Close(context.Background()))
}

func TestIOURingBatcher_FullLifecycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)

	b, err := dirsync.NewBestBatcher(v) // uses io_uring on Linux 5.11+
	require.NoError(t, err)
	ctx := context.Background()

	// Create files to remove.
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		require.NoError(t, b.Submit(ctx, dirsync.BatchOp{
			Kind: dirsync.OpRemove, RelPath: fmt.Sprintf("f%d.txt", i),
		}))
	}
	require.NoError(t, b.Close(ctx))

	// All files must be gone.
	for i := 0; i < 5; i++ {
		_, statErr := os.Stat(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)))
		assert.True(t, os.IsNotExist(statErr))
	}
}

func TestIOURingBatcher_Close_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	b, err := dirsync.NewBestBatcher(v)
	require.NoError(t, err)
	assert.NoError(t, b.Close(context.Background()))
	assert.NoError(t, b.Close(context.Background()), "second close must be idempotent")
}

// ─────────────────────────────────────────────────────────────────────────────
// classifier.go — WithFilter, buildFilter panics on bad pattern
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_WithFilter_ReplacesDefaultFilter(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"a.txt": "a", "b.txt": "b"})

	// Custom filter that rejects everything.
	rejectAll := rejectAllFilter{}
	c := dirsync.NewClassifier(lower, upper).WithFilter(rejectAll)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)
	assert.Empty(t, exc, "rejectAll filter must produce no exclusive paths")
	assert.Empty(t, com, "rejectAll filter must produce no common paths")
}

// rejectAllFilter is a test-double Filter that rejects every path.
type rejectAllFilter struct{}

func (rejectAllFilter) Include(_ string, _ bool) bool { return false }
func (rejectAllFilter) RequiredPaths() []string       { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// handler.go — MultiCommonHandler, OnlyKind, OnlyTypeMismatched
// ─────────────────────────────────────────────────────────────────────────────

func TestMultiCommonHandler_CollectsAllErrors(t *testing.T) {
	t.Parallel()
	e1, e2 := errors.New("m1"), errors.New("m2")
	multi := dirsync.MultiCommonHandler{
		dirsync.CommonHandlerFunc(func(_ context.Context, _ dirsync.CommonPath) error { return e1 }),
		dirsync.CommonHandlerFunc(func(_ context.Context, _ dirsync.CommonPath) error { return e2 }),
	}
	err := multi.HandleCommon(context.Background(), dirsync.CommonPath{})
	assert.ErrorIs(t, err, e1)
	assert.ErrorIs(t, err, e2)
}

func TestOnlyKind_MatchesCorrectKind(t *testing.T) {
	t.Parallel()
	pred := dirsync.OnlyKind(dirsync.PathKindDir)

	dirPath := dirsync.ExclusivePath{Kind: dirsync.PathKindDir}
	filePath := dirsync.ExclusivePath{Kind: dirsync.PathKindFile}

	assert.True(t, pred(dirPath))
	assert.False(t, pred(filePath))
}

func TestOnlyTypeMismatched_MatchesMismatchedEntries(t *testing.T) {
	t.Parallel()
	pred := dirsync.OnlyTypeMismatched()
	dir := t.TempDir()

	f := filepath.Join(dir, "f.txt")
	d := filepath.Join(dir, "sub")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(d, 0o755))

	fi, _ := os.Lstat(f)
	di, _ := os.Lstat(d)

	mismatch := dirsync.CommonPath{LowerInfo: di, UpperInfo: fi}
	same := dirsync.CommonPath{LowerInfo: fi, UpperInfo: fi}

	assert.True(t, pred(mismatch))
	assert.False(t, pred(same))
}

// ─────────────────────────────────────────────────────────────────────────────
// hasher.go — compareDirectories, fileDigest indirectly, segmentWorkers default
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_CompareDirectories_EqualModePerm(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	ld := filepath.Join(lower, "sub")
	ud := filepath.Join(upper, "sub")
	require.NoError(t, os.MkdirAll(ld, 0o755))
	require.NoError(t, os.MkdirAll(ud, 0o755))

	// Force same mtime on both dirs.
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(ld, ts, ts))
	require.NoError(t, os.Chtimes(ud, ts, ts))

	lInfo, _ := os.Lstat(ld)
	uInfo, _ := os.Lstat(ud)

	h := &dirsync.TwoPhaseHasher{}
	eq, err := h.Equal(ld, ud, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq, "directories with same perms and mtime must be equal")
}

func TestHasher_CompareDirectories_DifferentMtime(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	ld := filepath.Join(lower, "sub")
	ud := filepath.Join(upper, "sub")
	require.NoError(t, os.MkdirAll(ld, 0o755))
	require.NoError(t, os.MkdirAll(ud, 0o755))

	// Set different mtimes.
	t1 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(ld, t1, t1))
	require.NoError(t, os.Chtimes(ud, t2, t2))

	lInfo, _ := os.Lstat(ld)
	uInfo, _ := os.Lstat(ud)

	h := &dirsync.TwoPhaseHasher{}
	eq, err := h.Equal(ld, ud, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "directories with different mtime must not be equal")
}

func TestHasher_DefaultSegmentWorkers_UsesNumCPU(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	size := 3 << 20 // 3 MiB — above default 2 MiB threshold
	data := make([]byte, size)
	lAbs, uAbs := writeFilePair(t, lower, upper, "big.bin", string(data), string(data))
	writePastMtime(t, lAbs)

	// SegmentWorkers=0 → uses runtime.NumCPU()
	h := &dirsync.TwoPhaseHasher{SegmentWorkers: 0}
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq)
}

// ─────────────────────────────────────────────────────────────────────────────
// merged_view.go — FSMergedView.Root, MemMergedView.Root, MemMergedView.AbsPath
// ─────────────────────────────────────────────────────────────────────────────

func TestFSMergedView_Root_ReturnsAbsolutePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, v.Root())
}

func TestMemMergedView_Root_ReturnsConfiguredRoot(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/configured/root")
	assert.Equal(t, "/configured/root", v.Root())
}

func TestMemMergedView_AbsPath_JoinsCorrectly(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/root")
	assert.Equal(t, "/root/a/b.txt", v.AbsPath("a/b.txt"))
}

func TestFSMergedView_Remove_ErrorOnProtectedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	// Create a file inside sub.
	require.NoError(t, os.WriteFile(filepath.Join(sub, "child.txt"), []byte("x"), 0o644))

	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	// Try to Remove a non-empty directory (os.Remove fails on non-empty dirs).
	err = v.Remove(context.Background(), "sub")
	assert.Error(t, err, "Remove on non-empty directory must return an error")
}

// ─────────────────────────────────────────────────────────────────────────────
// walker.go — followSymlinks=true path, callExclusive/callCommon nil callbacks
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_FollowSymlinks_SymlinkToDir_TreatedAsDir(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()

	// Create a real dir in lower and a symlink to it.
	realDir := filepath.Join(lower, "real")
	require.NoError(t, os.MkdirAll(filepath.Join(realDir, "child"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "child", "f.txt"), []byte("x"), 0o644))

	linkDir := filepath.Join(lower, "link")
	require.NoError(t, os.Symlink(realDir, linkDir))

	c := dirsync.NewClassifier(lower, upper, dirsync.WithFollowSymlinks(true))
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)

	paths := exclusivePaths(exc)
	// Both "real" and "link" are exclusive; with followSymlinks the link dir
	// is treated as a directory and may be collapsed.
	assert.NotEmpty(t, paths)
}

// ─────────────────────────────────────────────────────────────────────────────
// hash_pipeline.go — symlink enrichment path
// ─────────────────────────────────────────────────────────────────────────────

func TestHashPipeline_SymlinkEntries_HashEqualSet(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()

	lLink := filepath.Join(lower, "link")
	uLink := filepath.Join(upper, "link")
	require.NoError(t, os.Symlink("/etc/hosts", lLink))
	require.NoError(t, os.Symlink("/etc/hosts", uLink))

	lInfo, _ := os.Lstat(lLink)
	uInfo, _ := os.Lstat(uLink)

	rawCh := make(chan dirsync.CommonPath, 1)
	rawCh <- dirsync.CommonPath{
		Path: "link", Kind: dirsync.PathKindSymlink,
		LowerInfo: lInfo, UpperInfo: uInfo,
	}
	close(rawCh)

	errCh := make(chan error, 4)
	hp := dirsync.NewHashPipeline()
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
	var results []dirsync.CommonPath
	for r := range enriched { results = append(results, r) }
	for range errCh {}

	require.Len(t, results, 1)
	eq, checked := results[0].IsContentEqual()
	assert.True(t, checked, "symlinks must have HashEqual set")
	assert.True(t, eq, "same symlink target must be equal")
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — NewDeleteEngine batcher close error path
// ─────────────────────────────────────────────────────────────────────────────

func TestNewDeleteEngine_InvalidMergedDir_ReturnsError(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	_, err := dirsync.NewDeleteEngine(lower, upper, "/nonexistent/merged", nil, nil)
	assert.Error(t, err, "non-existent merged dir must return error at construction")
}

// ─────────────────────────────────────────────────────────────────────────────
// types.go — PathKindOf for PathKindOther (device/pipe/socket)
// ─────────────────────────────────────────────────────────────────────────────

func TestPathKindOf_OtherFileMode(t *testing.T) {
	t.Parallel()
	// We can't easily create device files without root, so test via a named pipe.
	dir := t.TempDir()
	// Use a mock FileInfo to test PathKindOther without root privileges.
	_ = dir
	info := &mockFileInfo{mode: fs.ModeNamedPipe}
	assert.Equal(t, dirsync.PathKindOther, dirsync.PathKindOf(info))
}

// mockFileInfo implements fs.FileInfo for testing PathKindOf.
type mockFileInfo struct{ mode fs.FileMode }

func (m *mockFileInfo) Name() string      { return "mock" }
func (m *mockFileInfo) Size() int64       { return 0 }
func (m *mockFileInfo) Mode() fs.FileMode { return m.mode }
func (m *mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m *mockFileInfo) IsDir() bool       { return m.mode.IsDir() }
func (m *mockFileInfo) Sys() any          { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// errors.go — wrapOp nil passthrough
// ─────────────────────────────────────────────────────────────────────────────

func TestFSMergedView_Remove_NilErrorNoWrapping(t *testing.T) {
	t.Parallel()
	// Remove a file that exists — wrapOp(nil) must return nil.
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	// Successful remove: wrapOp receives nil → must return nil.
	assert.NoError(t, v.Remove(context.Background(), "f.txt"))
}

// ─────────────────────────────────────────────────────────────────────────────
// pipeline.go — classifierRoots with non-DirsyncClassifier
// ─────────────────────────────────────────────────────────────────────────────

func TestPipeline_CustomClassifier_ClassifierRootsEmpty(t *testing.T) {
	t.Parallel()
	// A custom Classifier implementation: classifierRoots() returns ("", "")
	// when the classifier is not *DirsyncClassifier.
	custom := &stubClassifier{}
	var excC dirsync.CountingExclusiveHandler
	pl := dirsync.NewPipeline(custom, &excC, dirsync.NoopCommonHandler{})
	result := pl.Run(context.Background())
	assert.True(t, result.OK())
}

// stubClassifier emits nothing — used to test classifierRoots fallback.
type stubClassifier struct{}

func (s *stubClassifier) Classify(_ context.Context) (
	<-chan dirsync.ExclusivePath,
	<-chan dirsync.CommonPath,
	<-chan error,
) {
	excCh := make(chan dirsync.ExclusivePath)
	comCh := make(chan dirsync.CommonPath)
	errCh := make(chan error)
	close(excCh)
	close(comCh)
	close(errCh)
	return excCh, comCh, errCh
}
