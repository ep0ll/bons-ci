package dirsync_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline integration tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPipeline_BasicDeleteFlow_RemovesExclusiveAndEqualCommon(t *testing.T) {
	t.Parallel()
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()

	makeTree(t, lower, map[string]string{
		"excl.txt":       "del",
		"excl-dir/a.txt": "del",
		"shared.txt":     "same",
	})
	makeTree(t, upper, map[string]string{"shared.txt": "same"})
	// merged starts as a copy of lower
	makeTree(t, merged, map[string]string{
		"excl.txt":       "del",
		"excl-dir/a.txt": "del",
		"shared.txt":     "same",
	})

	view, err := dirsync.NewFSMergedView(merged)
	require.NoError(t, err)
	batcher := dirsync.NewGoroutineBatcher(view, dirsync.WithAutoFlushAt(32))

	classifier := dirsync.NewClassifier(lower, upper)
	pl := dirsync.NewPipeline(classifier,
		dirsync.NoopExclusiveHandler{}, dirsync.NoopCommonHandler{},
		dirsync.WithExclusiveBatcher(batcher),
		dirsync.WithCommonBatcher(batcher),
	)
	result := pl.Run(context.Background())
	require.True(t, result.OK(), "pipeline error: %v", result.Err)
	require.NoError(t, batcher.Close(context.Background()))

	_, excErr := os.Stat(filepath.Join(merged, "excl.txt"))
	assert.True(t, os.IsNotExist(excErr), "excl.txt must be removed")

	_, dirErr := os.Stat(filepath.Join(merged, "excl-dir"))
	assert.True(t, os.IsNotExist(dirErr), "excl-dir must be removed (collapsed)")

	_, sharedErr := os.Stat(filepath.Join(merged, "shared.txt"))
	assert.True(t, os.IsNotExist(sharedErr), "shared.txt (equal) must be removed from merged")
}

func TestPipeline_ChangedCommonFile_NotRemoved(t *testing.T) {
	t.Parallel()
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()

	makeTree(t, lower, map[string]string{"f.txt": "lower-version"})
	makeTree(t, upper, map[string]string{"f.txt": "upper-version"})
	makeTree(t, merged, map[string]string{"f.txt": "upper-version"})

	// Force mtime difference so the hasher reads content.
	lPath := filepath.Join(lower, "f.txt")
	require.NoError(t, os.Chtimes(lPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)))

	view, err := dirsync.NewFSMergedView(merged)
	require.NoError(t, err)
	rb := &dirsync.RecordingBatcher{}

	classifier := dirsync.NewClassifier(lower, upper)
	pl := dirsync.NewPipeline(classifier,
		dirsync.NoopExclusiveHandler{}, dirsync.NoopCommonHandler{},
		dirsync.WithCommonBatcher(rb),
	)
	result := pl.Run(context.Background())
	require.True(t, result.OK(), "pipeline error: %v", result.Err)
	_ = view

	for _, op := range rb.Ops() {
		assert.NotEqual(t, "f.txt", op.RelPath, "changed file must NOT be batched for removal")
	}
}

func TestPipeline_ExclusiveBatcher_CorrectOpKind(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"a.txt":     "x",
		"dir/b.txt": "y",
	})

	rb := &dirsync.RecordingBatcher{}
	classifier := dirsync.NewClassifier(lower, upper)
	pl := dirsync.NewPipeline(classifier,
		dirsync.NoopExclusiveHandler{}, dirsync.NoopCommonHandler{},
		dirsync.WithExclusiveBatcher(rb),
	)
	result := pl.Run(context.Background())
	require.True(t, result.OK())
	require.NoError(t, rb.Flush(context.Background()))

	opMap := make(map[string]dirsync.OpKind)
	for _, op := range rb.Ops() { opMap[op.RelPath] = op.Kind }

	if kind, ok := opMap["dir"]; ok {
		assert.Equal(t, dirsync.OpRemoveAll, kind, "collapsed dir must use OpRemoveAll")
	}
	if kind, ok := opMap["a.txt"]; ok {
		assert.Equal(t, dirsync.OpRemove, kind, "leaf file must use OpRemove")
	}
}

func TestPipeline_AbortOnError_StopsEarly(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(lower, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644))
	}

	sentinel := errors.New("injected-error")
	var callCount atomic.Int32
	excHandler := dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error {
		callCount.Add(1)
		return sentinel
	})

	classifier := dirsync.NewClassifier(lower, upper)
	pl := dirsync.NewPipeline(classifier, excHandler, dirsync.NoopCommonHandler{},
		dirsync.WithAbortOnError(true),
		dirsync.WithExclusiveWorkers(1),
	)
	result := pl.Run(context.Background())
	assert.False(t, result.OK(), "pipeline must report error")
	assert.ErrorIs(t, result.Err, sentinel)
	// With abort-on-error and 1 worker, only a few handlers should fire.
	assert.Less(t, callCount.Load(), int32(20), "abort should have stopped processing early")
}

func TestPipeline_CollectsAllErrors_WhenAbortDisabled(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 5; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(lower, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644))
	}

	var callCount atomic.Int32
	excHandler := dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error {
		callCount.Add(1)
		return errors.New("always-fail")
	})

	classifier := dirsync.NewClassifier(lower, upper)
	pl := dirsync.NewPipeline(classifier, excHandler, dirsync.NoopCommonHandler{},
		dirsync.WithAbortOnError(false),
		dirsync.WithExclusiveWorkers(1),
	)
	result := pl.Run(context.Background())
	assert.False(t, result.OK())
	assert.Equal(t, int32(5), callCount.Load(), "all 5 handlers must be called without abort")
}

func TestPipeline_ContextCancellation_TerminatesCleanly(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 100; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(lower, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	classifier := dirsync.NewClassifier(lower, upper)
	pl := dirsync.NewPipeline(classifier, dirsync.NoopExclusiveHandler{}, dirsync.NoopCommonHandler{})

	done := make(chan struct{})
	go func() {
		pl.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not terminate after context cancellation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Engine integration tests
// ─────────────────────────────────────────────────────────────────────────────

func TestDeleteEngine_EndToEnd(t *testing.T) {
	t.Parallel()
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"excl.txt":   "del",
		"shared.txt": "same",
	})
	makeTree(t, upper, map[string]string{"shared.txt": "same"})
	makeTree(t, merged, map[string]string{
		"excl.txt":   "del",
		"shared.txt": "same",
	})

	eng, err := dirsync.NewDeleteEngine(lower, upper, merged, nil, nil)
	require.NoError(t, err)
	result := eng.Run(context.Background())
	require.True(t, result.OK(), "engine error: %v", result.Err)

	_, excErr := os.Stat(filepath.Join(merged, "excl.txt"))
	assert.True(t, os.IsNotExist(excErr), "excl.txt must be removed")
	_, sharedErr := os.Stat(filepath.Join(merged, "shared.txt"))
	assert.True(t, os.IsNotExist(sharedErr), "equal shared.txt must be removed")
}

func TestObserveEngine_NoMutations(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"excl.txt":   "e",
		"shared.txt": "s",
	})
	makeTree(t, upper, map[string]string{"shared.txt": "s"})

	var excC dirsync.CountingExclusiveHandler
	var comC dirsync.CountingCommonHandler
	eng := dirsync.NewObserveEngine(lower, upper, &excC, &comC, nil, nil)
	result := eng.Run(context.Background())
	require.True(t, result.OK(), "observe error: %v", result.Err)

	assert.Equal(t, int64(1), excC.Snapshot().Total(), "one exclusive path expected")
	assert.Equal(t, int64(1), comC.Snapshot().Total, "one common path expected")

	// Lower must not be touched.
	_, lErr := os.Stat(filepath.Join(lower, "excl.txt"))
	assert.NoError(t, lErr, "lower must not be mutated by ObserveEngine")
}

// ─────────────────────────────────────────────────────────────────────────────
// Full composition — chain + predicate routing
// ─────────────────────────────────────────────────────────────────────────────

func TestPipeline_FullComposition_ChainAndPredicateRouting(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"changed.txt": "version-A",
		"same.txt":    "stable",
		"excl.txt":    "exclusive",
	})
	makeTree(t, upper, map[string]string{
		"changed.txt": "version-B",
		"same.txt":    "stable",
	})
	// Force mtime difference so the hasher reads content.
	require.NoError(t, os.Chtimes(
		filepath.Join(lower, "changed.txt"),
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour),
	))

	var excCounter dirsync.CountingExclusiveHandler
	var changedCounter dirsync.CountingCommonHandler
	var unchangedCounter dirsync.CountingCommonHandler

	comHandler := dirsync.ChainCommonHandler{
		dirsync.PredicateCommonHandler{
			Predicate: dirsync.OnlyChanged(),
			Handler:   &changedCounter,
		},
		dirsync.PredicateCommonHandler{
			Predicate: dirsync.OnlyUnchanged(),
			Handler:   &unchangedCounter,
		},
	}

	classifier := dirsync.NewClassifier(lower, upper)
	pl := dirsync.NewPipeline(classifier, &excCounter, comHandler)
	result := pl.Run(context.Background())
	require.True(t, result.OK(), "pipeline error: %v", result.Err)

	assert.Equal(t, int64(1), excCounter.Snapshot().Total(), "one exclusive")
	assert.Equal(t, int64(1), changedCounter.Snapshot().Changed, "one changed common")
	assert.Equal(t, int64(1), unchangedCounter.Snapshot().Equal, "one unchanged common")
}
