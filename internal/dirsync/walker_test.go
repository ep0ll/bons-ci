package dirsync_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// makeTree creates files and directories inside root from a map of
// relative path → content. Paths ending in "/" are directories.
func makeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		if rel[len(rel)-1] == '/' {
			require.NoError(t, os.MkdirAll(abs, 0o755))
			continue
		}
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	}
}

// drainClassifier drains all three channels from Classify and returns slices.
func drainClassifier(
	t *testing.T, c dirsync.Classifier, timeout time.Duration,
) (exc []dirsync.ExclusivePath, com []dirsync.CommonPath, errs []error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	excCh, comCh, errCh := c.Classify(ctx)
	for excCh != nil || comCh != nil || errCh != nil {
		select {
		case ep, ok := <-excCh:
			if !ok { excCh = nil; continue }
			exc = append(exc, ep)
		case cp, ok := <-comCh:
			if !ok { comCh = nil; continue }
			com = append(com, cp)
		case err, ok := <-errCh:
			if !ok { errCh = nil; continue }
			errs = append(errs, err)
		}
	}
	return
}

func exclusivePaths(eps []dirsync.ExclusivePath) []string {
	paths := make([]string, len(eps))
	for i, ep := range eps { paths[i] = ep.Path }
	sort.Strings(paths)
	return paths
}

func commonPaths(cps []dirsync.CommonPath) []string {
	paths := make([]string, len(cps))
	for i, cp := range cps { paths[i] = cp.Path }
	sort.Strings(paths)
	return paths
}

func hasPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target { return true }
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Classifier — basic classification
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_AllExclusive_WhenUpperAbsent(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	require.NoError(t, os.RemoveAll(upper)) // upper does not exist

	makeTree(t, lower, map[string]string{
		"a.txt":     "hello",
		"sub/b.txt": "world",
	})

	c := dirsync.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)

	require.Empty(t, errs)
	require.Empty(t, com)

	paths := exclusivePaths(exc)
	assert.True(t, hasPath(paths, "a.txt"))
	assert.True(t, hasPath(paths, "sub"), "sub should be collapsed")
	assert.False(t, hasPath(paths, "sub/b.txt"), "child of collapsed dir must not be emitted")

	for _, ep := range exc {
		if ep.Path == "sub" {
			assert.True(t, ep.Collapsed, "sub must be Collapsed=true")
		}
	}
}

func TestClassifier_AllCommon_WhenTreesIdentical(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	files := map[string]string{"a.txt": "x", "sub/b.txt": "y"}
	makeTree(t, lower, files)
	makeTree(t, upper, files)

	c := dirsync.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)

	require.Empty(t, errs)
	require.Empty(t, exc)
	assert.True(t, hasPath(commonPaths(com), "a.txt"))
	assert.True(t, hasPath(commonPaths(com), "sub/b.txt"))
}

func TestClassifier_Mixed_LowerAndUpperPartialOverlap(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"shared.txt":      "same",
		"lower-only.txt":  "excl",
		"lower-dir/a.txt": "excl-child",
	})
	makeTree(t, upper, map[string]string{
		"shared.txt":     "same",
		"upper-only.txt": "upper",
	})

	c := dirsync.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)

	require.Empty(t, errs)
	ep := exclusivePaths(exc)
	cp := commonPaths(com)

	assert.True(t, hasPath(ep, "lower-only.txt"))
	assert.True(t, hasPath(ep, "lower-dir"))
	assert.False(t, hasPath(ep, "lower-dir/a.txt"), "child of collapsed")
	assert.False(t, hasPath(ep, "upper-only.txt"), "upper-only must not appear in exclusive")

	assert.True(t, hasPath(cp, "shared.txt"))
	assert.False(t, hasPath(cp, "upper-only.txt"))
}

// ─────────────────────────────────────────────────────────────────────────────
// Classifier — collapse semantics
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_CollapseDeepExclusiveSubtree(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"shared/common.txt":     "c",
		"shared/excl-dir/a.txt": "e",
		"shared/excl-dir/b.txt": "e",
	})
	makeTree(t, upper, map[string]string{"shared/common.txt": "c"})

	c := dirsync.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)

	require.Empty(t, errs)
	ep := exclusivePaths(exc)
	assert.True(t, hasPath(ep, "shared/excl-dir"))
	assert.False(t, hasPath(ep, "shared/excl-dir/a.txt"), "child of collapsed")
	assert.True(t, hasPath(commonPaths(com), "shared/common.txt"))
}

// ─────────────────────────────────────────────────────────────────────────────
// Classifier — BuildKit type mismatch (overlay semantics)
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_BuildKit_TypeMismatch_DirReplacedByFile(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"x/child.txt": "orphaned"})
	makeTree(t, upper, map[string]string{"x": "file-replacing-dir"})

	c := dirsync.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)

	require.Empty(t, errs)

	// "x" must appear as common (type mismatch)
	assert.True(t, hasPath(commonPaths(com), "x"))
	var xEntry *dirsync.CommonPath
	for i := range com {
		if com[i].Path == "x" { xEntry = &com[i] }
	}
	require.NotNil(t, xEntry)
	assert.True(t, xEntry.TypeMismatch())

	// "x" must also appear as collapsed exclusive (orphaned subtree)
	assert.True(t, hasPath(exclusivePaths(exc), "x"))
	assert.False(t, hasPath(exclusivePaths(exc), "x/child.txt"), "child of collapsed")
}

// ─────────────────────────────────────────────────────────────────────────────
// Classifier — filtering
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_IncludePattern_OnlyMatchingPaths(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"keep/a.go":   "go",
		"skip/b.txt":  "txt",
		"skip-me.txt": "txt",
	})

	c := dirsync.NewClassifier(lower, upper, dirsync.WithIncludePatterns("keep"))
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)

	ep := exclusivePaths(exc)
	assert.True(t, hasPath(ep, "keep"))
	assert.False(t, hasPath(ep, "skip"), "excluded by include pattern")
	assert.False(t, hasPath(ep, "skip-me.txt"))
}

func TestClassifier_ExcludePattern_RemovesMatchingPaths(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"vendor/dep.go": "dep",
		"main.go":       "main",
	})

	c := dirsync.NewClassifier(lower, upper, dirsync.WithExcludePatterns("vendor"))
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)

	ep := exclusivePaths(exc)
	assert.True(t, hasPath(ep, "main.go"))
	assert.False(t, hasPath(ep, "vendor"), "vendor excluded")
}

func TestClassifier_RequiredPaths_MissingPathReturnsError(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"a.txt": "a"})

	c := dirsync.NewClassifier(lower, upper, dirsync.WithRequiredPaths("missing.txt"))
	_, _, errs := drainClassifier(t, c, 5*time.Second)

	require.NotEmpty(t, errs)
	assert.ErrorIs(t, errs[0], dirsync.ErrRequiredPathMissing)
}

func TestClassifier_RequiredPaths_PresentPathNoError(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"req.txt": "yes"})

	c := dirsync.NewClassifier(lower, upper, dirsync.WithRequiredPaths("req.txt"))
	_, _, errs := drainClassifier(t, c, 5*time.Second)
	assert.Empty(t, errs)
}

// ─────────────────────────────────────────────────────────────────────────────
// Classifier — context cancellation (no goroutine leaks)
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_ContextCancellation_ChannelsClose(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 50; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(lower, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	c := dirsync.NewClassifier(lower, upper)
	excCh, comCh, errCh := c.Classify(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for excCh != nil || comCh != nil || errCh != nil {
			select {
			case _, ok := <-excCh:
				if !ok { excCh = nil }
			case _, ok := <-comCh:
				if !ok { comCh = nil }
			case _, ok := <-errCh:
				if !ok { errCh = nil }
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("goroutines did not exit after context cancellation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Classifier — concurrent safety
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_ConcurrentDrain_RaceDetector(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{
		"a.txt": "a", "b.txt": "b", "c.txt": "c",
	})

	c := dirsync.NewClassifier(lower, upper)
	ctx := context.Background()
	excCh, comCh, errCh := c.Classify(ctx)

	var excCount, comCount atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for excCh != nil || comCh != nil || errCh != nil {
			select {
			case _, ok := <-excCh:
				if !ok { excCh = nil; continue }
				excCount.Add(1)
			case _, ok := <-comCh:
				if !ok { comCh = nil; continue }
				comCount.Add(1)
			case _, ok := <-errCh:
				if !ok { errCh = nil }
			}
		}
	}()
	<-done
	assert.Equal(t, int64(3), excCount.Load())
}

// ─────────────────────────────────────────────────────────────────────────────
// Classifier — path security (no traversal)
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_PathTraversal_DoesNotEscapeRoot(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	// Create a file in a sub-dir; the traversal must not escape lower.
	makeTree(t, lower, map[string]string{"legit/file.txt": "content"})

	c := dirsync.NewClassifier(lower, upper)
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	require.Empty(t, errs)

	for _, ep := range exc {
		assert.NotContains(t, ep.Path, "..", "path must not contain traversal components")
	}
}
