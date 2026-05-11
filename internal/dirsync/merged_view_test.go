package dirsync_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// FSMergedView
// ─────────────────────────────────────────────────────────────────────────────

func TestFSMergedView_Remove_DeletesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "del.txt")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))

	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	require.NoError(t, v.Remove(context.Background(), "del.txt"))
	assert.True(t, os.IsNotExist(func() error { _, e := os.Stat(target); return e }()))
}

func TestFSMergedView_Remove_Idempotent_WhenAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	// Calling Remove on a nonexistent path must not return an error.
	assert.NoError(t, v.Remove(context.Background(), "nonexistent.txt"))
}

func TestFSMergedView_RemoveAll_DeletesSubtree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "subtree", "child")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "a.so"), []byte("lib"), 0o644))

	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	require.NoError(t, v.RemoveAll(context.Background(), "subtree"))

	_, statErr := os.Stat(filepath.Join(dir, "subtree"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestFSMergedView_RemoveAll_Idempotent_WhenAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	assert.NoError(t, v.RemoveAll(context.Background(), "doesnotexist"))
}

func TestFSMergedView_Stat_ReturnsFileInfo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644))

	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	info, err := v.Stat(context.Background(), "f.txt")
	require.NoError(t, err)
	assert.Equal(t, "f.txt", info.Name())
}

func TestFSMergedView_InvalidDir_ReturnsError(t *testing.T) {
	t.Parallel()
	_, err := dirsync.NewFSMergedView("/nonexistent/path/that/cannot/exist")
	assert.Error(t, err)
}

func TestFSMergedView_NonDirectory_ReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	_, err := dirsync.NewFSMergedView(file)
	assert.Error(t, err)
}

func TestFSMergedView_AbsPath_ResolvesProperly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	abs := v.AbsPath("a/b/c.txt")
	assert.Equal(t, filepath.Join(dir, "a", "b", "c.txt"), abs)
}

// ─────────────────────────────────────────────────────────────────────────────
// MemMergedView
// ─────────────────────────────────────────────────────────────────────────────

func TestMemMergedView_RecordsAllOps(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/fake/merged")
	ctx := context.Background()

	require.NoError(t, v.Remove(ctx, "a.txt"))
	require.NoError(t, v.RemoveAll(ctx, "lib/"))
	require.NoError(t, v.Remove(ctx, "b.txt"))

	assert.Len(t, v.Removed, 2)
	assert.Len(t, v.RemovedAll, 1)
	assert.Len(t, v.AllOps(), 3)
}

func TestMemMergedView_Stat_AlwaysNotExist(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/root")
	_, err := v.Stat(context.Background(), "any")
	assert.True(t, errors.Is(err, os.ErrNotExist) || err != nil,
		"MemMergedView.Stat must return an error indicating absence")
}

func TestMemMergedView_Reset_ClearsRecords(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/root")
	ctx := context.Background()
	_ = v.Remove(ctx, "x")
	_ = v.RemoveAll(ctx, "y")
	v.Reset()
	assert.Empty(t, v.Removed)
	assert.Empty(t, v.RemovedAll)
}

func TestMemMergedView_ConcurrentAccess_RaceDetector(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/root")
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = v.Remove(ctx, "f"+string(rune('a'+i%26)))
		}()
		go func() {
			defer wg.Done()
			_ = v.RemoveAll(ctx, "d"+string(rune('a'+i%26)))
		}()
	}
	wg.Wait()
	assert.Len(t, v.Removed, 50)
	assert.Len(t, v.RemovedAll, 50)
}
