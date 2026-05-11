package dirsync_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lstatPair(t *testing.T, lDir, uDir, name string) (dirsync.CommonPath) {
	t.Helper()
	lInfo, err := os.Lstat(filepath.Join(lDir, name))
	require.NoError(t, err)
	uInfo, err := os.Lstat(filepath.Join(uDir, name))
	require.NoError(t, err)
	return dirsync.CommonPath{
		Path:      name,
		Kind:      dirsync.PathKindFile,
		LowerInfo: lInfo,
		UpperInfo: uInfo,
	}
}

func runHashPipeline(
	t *testing.T,
	lower, upper string,
	cps []dirsync.CommonPath,
	opts ...dirsync.HashPipelineOption,
) []dirsync.CommonPath {
	t.Helper()
	rawCh := make(chan dirsync.CommonPath, len(cps))
	for _, cp := range cps { rawCh <- cp }
	close(rawCh)

	errCh := make(chan error, 16)
	hp := dirsync.NewHashPipeline(opts...)
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)

	var results []dirsync.CommonPath
	for cp := range enriched { results = append(results, cp) }
	for range errCh {}
	return results
}

// ─────────────────────────────────────────────────────────────────────────────
// HashPipeline — enrichment correctness
// ─────────────────────────────────────────────────────────────────────────────

func TestHashPipeline_EqualFiles_HashEqualTrue(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	writeFilePair(t, lower, upper, "f.txt", "identical", "identical")
	writePastMtime(t, filepath.Join(lower, "f.txt"))

	cp := lstatPair(t, lower, upper, "f.txt")
	results := runHashPipeline(t, lower, upper, []dirsync.CommonPath{cp})
	require.Len(t, results, 1)
	eq, checked := results[0].IsContentEqual()
	assert.True(t, checked, "hash must be checked for regular files")
	assert.True(t, eq)
}

func TestHashPipeline_DifferentFiles_HashEqualFalse(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	writeFilePair(t, lower, upper, "f.txt", "version-A", "version-B")
	writePastMtime(t, filepath.Join(lower, "f.txt"))

	cp := lstatPair(t, lower, upper, "f.txt")
	results := runHashPipeline(t, lower, upper, []dirsync.CommonPath{cp})
	require.Len(t, results, 1)
	eq, checked := results[0].IsContentEqual()
	assert.True(t, checked)
	assert.False(t, eq)
}

func TestHashPipeline_DirectoryEntries_HashEqualNil(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(lower, "sub"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(upper, "sub"), 0o755))

	lInfo, _ := os.Lstat(filepath.Join(lower, "sub"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "sub"))
	cp := dirsync.CommonPath{
		Path: "sub", Kind: dirsync.PathKindDir,
		LowerInfo: lInfo, UpperInfo: uInfo,
	}

	rawCh := make(chan dirsync.CommonPath, 1)
	rawCh <- cp
	close(rawCh)
	errCh := make(chan error, 4)
	hp := dirsync.NewHashPipeline()
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)

	var results []dirsync.CommonPath
	for r := range enriched { results = append(results, r) }
	for range errCh {}

	require.Len(t, results, 1)
	_, checked := results[0].IsContentEqual()
	assert.False(t, checked, "directories must not have HashEqual set")
}

func TestHashPipeline_ParallelHashing_AllCorrect(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	const N = 20
	var cps []dirsync.CommonPath
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		content := fmt.Sprintf("content-%d", i)
		writeFilePair(t, lower, upper, name, content, content)
		writePastMtime(t, filepath.Join(lower, name))
		cps = append(cps, lstatPair(t, lower, upper, name))
	}

	results := runHashPipeline(t, lower, upper, cps, dirsync.WithHashWorkers(8))
	require.Len(t, results, N)
	for _, r := range results {
		eq, checked := r.IsContentEqual()
		assert.True(t, checked, "all files must be checked: %s", r.Path)
		assert.True(t, eq, "all identical files must be equal: %s", r.Path)
	}
}

func TestHashPipeline_ContextCancellation_Drains(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	const N = 50
	var cps []dirsync.CommonPath
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		writeFilePair(t, lower, upper, name, "x", "x")
		writePastMtime(t, filepath.Join(lower, name))
		cps = append(cps, lstatPair(t, lower, upper, name))
	}

	rawCh := make(chan dirsync.CommonPath, N)
	for _, cp := range cps { rawCh <- cp }
	close(rawCh)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	errCh := make(chan error, 16)
	hp := dirsync.NewHashPipeline()
	enriched := hp.Run(ctx, lower, upper, rawCh, errCh)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range enriched {}
		for range errCh {}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("HashPipeline goroutines did not exit after context cancellation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HashPipeline benchmark
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkHashPipeline_100Files_Parallel(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	const N = 100
	var cps []dirsync.CommonPath
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		content := fmt.Sprintf("bench-content-%d-padding-to-make-it-bigger", i)
		_ = os.WriteFile(filepath.Join(lower, name), []byte(content), 0o644)
		_ = os.WriteFile(filepath.Join(upper, name), []byte(content), 0o644)
		past := time.Now().Add(-time.Hour)
		_ = os.Chtimes(filepath.Join(lower, name), past, past)
		lInfo, _ := os.Lstat(filepath.Join(lower, name))
		uInfo, _ := os.Lstat(filepath.Join(upper, name))
		cps = append(cps, dirsync.CommonPath{
			Path: name, Kind: dirsync.PathKindFile,
			LowerInfo: lInfo, UpperInfo: uInfo,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rawCh := make(chan dirsync.CommonPath, N)
		for _, cp := range cps { rawCh <- cp }
		close(rawCh)
		errCh := make(chan error, 16)
		hp := dirsync.NewHashPipeline(dirsync.WithHashWorkers(8))
		enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
		for range enriched {}
		for range errCh {}
	}
}
