package dirsync_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// ClassifierOptions — functional smoke tests
// ─────────────────────────────────────────────────────────────────────────────

func TestWithFollowSymlinks_OptionApplied(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"a.txt": "x"})

	c := dirsync.NewClassifier(lower, upper, dirsync.WithFollowSymlinks(true))
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)
	assert.Len(t, exc, 1)
}

func TestWithAllowWildcards_IsNoOp(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"main.go": "go"})

	c := dirsync.NewClassifier(lower, upper,
		dirsync.WithAllowWildcards(true),
		dirsync.WithIncludePatterns("*.go"),
	)
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)
	assert.Len(t, exc, 1)
}

func TestWithExclusiveBufferSize_LargerBufferNoDeadlock(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 200; i++ {
		makeTree(t, lower, map[string]string{
			fmt.Sprintf("file%04d.txt", i): "x",
		})
	}
	c := dirsync.NewClassifier(lower, upper, dirsync.WithExclusiveBufferSize(512))
	exc, _, errs := drainClassifier(t, c, 10*time.Second)
	require.Empty(t, errs)
	assert.Len(t, exc, 200)
}

func TestWithCommonBufferSize_SmallBufferNoDeadlock(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	files := map[string]string{"a.txt": "a", "b.txt": "b"}
	makeTree(t, lower, files)
	makeTree(t, upper, files)

	c := dirsync.NewClassifier(lower, upper, dirsync.WithCommonBufferSize(1))
	_, com, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)
	assert.Len(t, com, 2)
}

// ─────────────────────────────────────────────────────────────────────────────
// PipelineOptions — functional smoke tests
// ─────────────────────────────────────────────────────────────────────────────

func TestWithExclusiveWorkers_SingleWorker(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"a.txt": "a", "b.txt": "b"})

	rb := &dirsync.RecordingBatcher{}
	pl := dirsync.NewPipeline(
		dirsync.NewClassifier(lower, upper),
		dirsync.NoopExclusiveHandler{}, dirsync.NoopCommonHandler{},
		dirsync.WithExclusiveWorkers(1),
		dirsync.WithExclusiveBatcher(rb),
	)
	result := pl.Run(context.Background())
	require.True(t, result.OK())
	assert.Len(t, rb.Ops(), 2)
}

func TestWithCommonWorkers_SingleWorker(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	files := map[string]string{"a.txt": "a", "b.txt": "b"}
	makeTree(t, lower, files)
	makeTree(t, upper, files)

	var count dirsync.CountingCommonHandler
	pl := dirsync.NewPipeline(
		dirsync.NewClassifier(lower, upper),
		dirsync.NoopExclusiveHandler{}, &count,
		dirsync.WithCommonWorkers(1),
	)
	result := pl.Run(context.Background())
	require.True(t, result.OK())
	assert.Equal(t, int64(2), count.Snapshot().Total)
}

func TestWithHashPipeline_CustomHasher(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	files := map[string]string{"a.txt": "same", "b.txt": "same"}
	makeTree(t, lower, files)
	makeTree(t, upper, files)

	customHasher := dirsync.NewHashPipeline(dirsync.WithHashWorkers(2))
	pl := dirsync.NewPipeline(
		dirsync.NewClassifier(lower, upper),
		dirsync.NoopExclusiveHandler{}, dirsync.NoopCommonHandler{},
		dirsync.WithHashPipeline(customHasher),
	)
	result := pl.Run(context.Background())
	require.True(t, result.OK())
}

func TestNewCustomEngine_UsesProvidedComponents(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"x.txt": "x"})

	var count dirsync.CountingExclusiveHandler
	eng := dirsync.NewCustomEngine(
		dirsync.NewClassifier(lower, upper),
		&count, dirsync.NoopCommonHandler{},
	)
	result := eng.Run(context.Background())
	require.True(t, result.OK())
	assert.Equal(t, int64(1), count.Snapshot().Total())
}

func TestWithBufPool_CustomPool(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	writeFilePair(t, lower, upper, "f.txt", "identical", "identical")
	writePastMtime(t, lower+"/f.txt")

	lInfo := lstatInfo(t, lower+"/f.txt")
	uInfo := lstatInfo(t, upper+"/f.txt")

	customPool := dirsync.NewBufPool(4096)
	hp := dirsync.NewHashPipeline(dirsync.WithBufPool(customPool))

	rawCh := make(chan dirsync.CommonPath, 1)
	rawCh <- dirsync.CommonPath{
		Path: "f.txt", Kind: dirsync.PathKindFile,
		LowerInfo: lInfo, UpperInfo: uInfo,
	}
	close(rawCh)

	errCh := make(chan error, 4)
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
	var results []dirsync.CommonPath
	for r := range enriched { results = append(results, r) }
	for range errCh {}

	require.Len(t, results, 1)
	eq, checked := results[0].IsContentEqual()
	assert.True(t, checked)
	assert.True(t, eq)
}

func TestWithHasher_CustomHasher(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	writeFilePair(t, lower, upper, "f.txt", "same", "same")
	writePastMtime(t, lower+"/f.txt")

	lInfo := lstatInfo(t, lower+"/f.txt")
	uInfo := lstatInfo(t, upper+"/f.txt")

	// Inject a custom hasher that always says equal.
	alwaysEqual := &alwaysEqualHasher{}
	hp := dirsync.NewHashPipeline(dirsync.WithHasher(alwaysEqual))

	rawCh := make(chan dirsync.CommonPath, 1)
	rawCh <- dirsync.CommonPath{
		Path: "f.txt", Kind: dirsync.PathKindFile,
		LowerInfo: lInfo, UpperInfo: uInfo,
	}
	close(rawCh)

	errCh := make(chan error, 4)
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
	var results []dirsync.CommonPath
	for r := range enriched { results = append(results, r) }
	for range errCh {}

	require.Len(t, results, 1)
	eq, checked := results[0].IsContentEqual()
	assert.True(t, checked)
	assert.True(t, eq)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func lstatInfo(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	return info
}

// alwaysEqualHasher is a test-double ContentHasher that always returns true.
type alwaysEqualHasher struct{}

func (a *alwaysEqualHasher) Equal(_, _ string, _, _ fs.FileInfo) (bool, error) {
	return true, nil
}
