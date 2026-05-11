package dirsync_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// GoroutineBatcher — unit tests
// ─────────────────────────────────────────────────────────────────────────────

func TestGoroutineBatcher_SubmitAndFlush(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/merged")
	b := dirsync.NewGoroutineBatcher(v)
	ctx := context.Background()

	for _, p := range []string{"a", "b", "c"} {
		require.NoError(t, b.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: p}))
	}
	require.NoError(t, b.Flush(ctx))
	assert.Len(t, v.Removed, 3)
}

func TestGoroutineBatcher_AutoFlush_TriggersAtThreshold(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/merged")
	b := dirsync.NewGoroutineBatcher(v, dirsync.WithAutoFlushAt(3))
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		require.NoError(t, b.Submit(ctx, dirsync.BatchOp{
			Kind:    dirsync.OpRemove,
			RelPath: fmt.Sprintf("f%d", i),
		}))
	}
	// Auto-flush fires at 3 and 6; at least 3 removes should have executed.
	assert.GreaterOrEqual(t, len(v.Removed), 3)
	require.NoError(t, b.Close(ctx))
}

func TestGoroutineBatcher_RemoveAll_DeletesSubtree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "lib", "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "a.so"), []byte("x"), 0o644))

	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	b := dirsync.NewGoroutineBatcher(v)
	ctx := context.Background()

	require.NoError(t, b.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemoveAll, RelPath: "lib"}))
	require.NoError(t, b.Close(ctx))

	_, statErr := os.Stat(filepath.Join(dir, "lib"))
	assert.True(t, os.IsNotExist(statErr), "lib subtree should be fully removed")
}

func TestGoroutineBatcher_Close_Idempotent(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/m")
	b := dirsync.NewGoroutineBatcher(v)
	ctx := context.Background()

	require.NoError(t, b.Close(ctx), "first Close must succeed")
	require.NoError(t, b.Close(ctx), "second Close must also succeed (idempotent)")
}

func TestGoroutineBatcher_SubmitAfterClose_ReturnsError(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/m")
	b := dirsync.NewGoroutineBatcher(v)
	ctx := context.Background()

	require.NoError(t, b.Close(ctx))
	err := b.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, dirsync.ErrBatcherClosed)
}

func TestGoroutineBatcher_ConcurrentSubmits_AllExecuted(t *testing.T) {
	t.Parallel()
	v := dirsync.NewMemMergedView("/merged")
	b := dirsync.NewGoroutineBatcher(v)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Submit(ctx, dirsync.BatchOp{
				Kind:    dirsync.OpRemove,
				RelPath: fmt.Sprintf("f%d", i),
			})
		}()
	}
	wg.Wait()
	require.NoError(t, b.Close(ctx))
	assert.Len(t, v.Removed, 100, "all 100 concurrent removes must be executed")
}

func TestGoroutineBatcher_ContextCancellation_ExitsGracefully(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := dirsync.NewFSMergedView(dir)
	require.NoError(t, err)
	b := dirsync.NewGoroutineBatcher(v)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Submit should not panic or deadlock with a cancelled context.
	_ = b.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: "x"})
	_ = b.Flush(ctx)
	_ = b.Close(ctx)
}

// ─────────────────────────────────────────────────────────────────────────────
// RecordingBatcher — test double
// ─────────────────────────────────────────────────────────────────────────────

func TestRecordingBatcher_PermanentLogPreservedAfterFlush(t *testing.T) {
	t.Parallel()
	rb := &dirsync.RecordingBatcher{}
	ctx := context.Background()

	for _, p := range []string{"x", "y", "z"} {
		require.NoError(t, rb.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: p}))
	}
	require.Len(t, rb.Ops(), 3)
	require.NoError(t, rb.Flush(ctx))

	assert.Equal(t, int64(3), rb.Total(), "total must reflect flushed count")
	assert.Len(t, rb.Ops(), 3, "permanent log must survive flush")
	assert.Empty(t, rb.Pending(), "pending must be empty after flush")
}

func TestRecordingBatcher_ConcurrentSubmits_Safe(t *testing.T) {
	t.Parallel()
	rb := &dirsync.RecordingBatcher{}
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rb.Submit(ctx, dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: fmt.Sprintf("f%d", i)})
		}()
	}
	wg.Wait()
	assert.Len(t, rb.Ops(), 50)
}

// ─────────────────────────────────────────────────────────────────────────────
// NopBatcher
// ─────────────────────────────────────────────────────────────────────────────

func TestNopBatcher_AllMethodsSucceed(t *testing.T) {
	t.Parallel()
	b := dirsync.NopBatcher{}
	ctx := context.Background()
	assert.NoError(t, b.Submit(ctx, dirsync.BatchOp{}))
	assert.NoError(t, b.Flush(ctx))
	assert.NoError(t, b.Close(ctx))
}

// ─────────────────────────────────────────────────────────────────────────────
// GoroutineBatcher benchmark
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkGoroutineBatcher_1000Ops_Parallel(b *testing.B) {
	dir := b.TempDir()
	// Pre-create files so Remove actually finds something.
	for i := 0; i < 1000; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o644)
	}
	v, _ := dirsync.NewFSMergedView(dir)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Re-create files between iterations.
		for j := 0; j < 1000; j++ {
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", j)), []byte("x"), 0o644)
		}
		batcher := dirsync.NewGoroutineBatcher(v)
		b.StartTimer()

		ctx := context.Background()
		for j := 0; j < 1000; j++ {
			_ = batcher.Submit(ctx, dirsync.BatchOp{
				Kind:    dirsync.OpRemove,
				RelPath: fmt.Sprintf("f%04d.txt", j),
			})
		}
		_ = batcher.Close(ctx)
	}
}
