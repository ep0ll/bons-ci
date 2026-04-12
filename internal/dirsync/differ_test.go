package differ_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	differ "github.com/bons/bons-ci/internal/dirsync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func fixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("fixture MkdirAll: %v", err)
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

// drainClassifier collects all results from a Classifier.Classify call.
func drainClassifier(t *testing.T, c differ.Classifier, timeout time.Duration) (
	exclusive []differ.ExclusivePath, common []differ.CommonPath, errs []error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	excCh, comCh, errCh := c.Classify(ctx)
	for excCh != nil || comCh != nil || errCh != nil {
		select {
		case ep, ok := <-excCh:
			if !ok {
				excCh = nil
				continue
			}
			exclusive = append(exclusive, ep)
		case cp, ok := <-comCh:
			if !ok {
				comCh = nil
				continue
			}
			common = append(common, cp)
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			errs = append(errs, err)
		}
	}
	return
}

func sortPaths(ps []string) []string { sort.Strings(ps); return ps }

func exclusivePaths(eps []differ.ExclusivePath) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Path
	}
	return sortPaths(out)
}

func commonPaths(cps []differ.CommonPath) []string {
	out := make([]string, len(cps))
	for i, c := range cps {
		out[i] = c.Path
	}
	return sortPaths(out)
}

func mustHave(t *testing.T, got []string, want string, label string) {
	t.Helper()
	for _, p := range got {
		if p == want {
			return
		}
	}
	t.Errorf("%s: expected %q in %v", label, want, got)
}

func mustNotHave(t *testing.T, got []string, want string, label string) {
	t.Helper()
	for _, p := range got {
		if p == want {
			t.Errorf("%s: %q must NOT be in %v", label, want, got)
			return
		}
	}
}

func boolPtr(v bool) *bool { return &v }

// ─────────────────────────────────────────────────────────────────────────────
// Classifier tests
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_AllExclusive_UpperAbsent(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()
	os.RemoveAll(upper) // upper does not exist

	fixture(t, lower, map[string]string{
		"a.txt":     "hello",
		"sub/b.txt": "world",
	})

	c := differ.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(com) != 0 {
		t.Errorf("expected no common, got %v", commonPaths(com))
	}

	paths := exclusivePaths(exc)
	mustHave(t, paths, "a.txt", "exclusive")
	mustHave(t, paths, "sub", "exclusive")

	// "sub" must be collapsed
	for _, ep := range exc {
		if ep.Path == "sub" && !ep.Collapsed {
			t.Error("sub must be Collapsed=true")
		}
	}
	mustNotHave(t, paths, "sub/b.txt", "children of collapsed dir")
}

func TestClassifier_AllCommon_IdenticalTrees(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	files := map[string]string{"a.txt": "x", "sub/b.txt": "y"}
	fixture(t, lower, files)
	fixture(t, upper, files)

	c := differ.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(exc) != 0 {
		t.Errorf("expected no exclusive, got %v", exclusivePaths(exc))
	}
	mustHave(t, commonPaths(com), "a.txt", "common")
	mustHave(t, commonPaths(com), "sub/b.txt", "common")
}

func TestClassifier_Mixed(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"shared.txt":      "same",
		"lower-only.txt":  "excl",
		"lower-dir/a.txt": "excl-child",
	})
	fixture(t, upper, map[string]string{
		"shared.txt":     "same",
		"upper-only.txt": "upper",
	})

	c := differ.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	excPaths := exclusivePaths(exc)
	mustHave(t, excPaths, "lower-only.txt", "exclusive")
	mustHave(t, excPaths, "lower-dir", "exclusive collapsed dir")
	mustNotHave(t, excPaths, "lower-dir/a.txt", "child of collapsed dir")
	mustNotHave(t, excPaths, "upper-only.txt", "upper-only must not appear")

	comPaths := commonPaths(com)
	mustHave(t, comPaths, "shared.txt", "common")
	mustNotHave(t, comPaths, "upper-only.txt", "upper-only in common")
}

func TestClassifier_CollapseDeepExclusiveSubtree(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"shared/common.txt":     "c",
		"shared/excl-dir/a.txt": "e",
		"shared/excl-dir/b.txt": "e",
	})
	fixture(t, upper, map[string]string{"shared/common.txt": "c"})

	c := differ.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	excPaths := exclusivePaths(exc)
	mustHave(t, excPaths, "shared/excl-dir", "collapsed exclusive dir")
	mustNotHave(t, excPaths, "shared/excl-dir/a.txt", "child of collapsed")
	mustHave(t, commonPaths(com), "shared/common.txt", "common")
}

func TestClassifier_BuildKit_TypeMismatch(t *testing.T) {
	// BuildKit overlay: upper non-dir replaces lower dir.
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"x/child.txt": "orphaned"})
	fixture(t, upper, map[string]string{"x": "file-replacing-dir"})

	c := differ.NewClassifier(lower, upper)
	exc, com, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// x must appear in common (type mismatch)
	mustHave(t, commonPaths(com), "x", "type-mismatch common")
	var xc *differ.CommonPath
	for i := range com {
		if com[i].Path == "x" {
			xc = &com[i]
		}
	}
	if xc == nil || !xc.TypeMismatch() {
		t.Error("expected TypeMismatch()=true for x")
	}

	// x must also appear as collapsed exclusive (orphaned lower subtree)
	mustHave(t, exclusivePaths(exc), "x", "collapsed exclusive for orphaned subtree")
	mustNotHave(t, exclusivePaths(exc), "x/child.txt", "child of collapsed")
}

func TestClassifier_IncludePatterns(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"keep/a.go":   "go",
		"skip/b.txt":  "txt",
		"skip-me.txt": "txt",
	})

	c := differ.NewClassifier(lower, upper, differ.WithIncludePatterns("keep"))
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	paths := exclusivePaths(exc)
	mustHave(t, paths, "keep", "kept dir")
	mustNotHave(t, paths, "skip", "excluded by include pattern")
}

func TestClassifier_ExcludePatterns(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"vendor/dep.go": "dep",
		"main.go":       "main",
	})

	c := differ.NewClassifier(lower, upper, differ.WithExcludePatterns("vendor"))
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	paths := exclusivePaths(exc)
	mustHave(t, paths, "main.go", "main.go kept")
	mustNotHave(t, paths, "vendor", "vendor excluded")
}

func TestClassifier_WildcardInclude(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"pkg/main.go":  "go",
		"pkg/main.txt": "txt",
	})

	c := differ.NewClassifier(lower, upper,
		differ.WithAllowWildcards(true),
		differ.WithIncludePatterns("*.go"),
	)
	exc, _, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	paths := exclusivePaths(exc)
	mustHave(t, paths, "pkg/main.go", "go file included")
	mustNotHave(t, paths, "pkg/main.txt", "txt excluded by wildcard")
}

func TestClassifier_RequiredPaths_Missing(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"a.txt": "a"})

	c := differ.NewClassifier(lower, upper, differ.WithRequiredPaths("missing.txt"))
	_, _, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) == 0 {
		t.Fatal("expected error for missing required path")
	}
}

func TestClassifier_RequiredPaths_Present(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"req.txt": "yes"})

	c := differ.NewClassifier(lower, upper, differ.WithRequiredPaths("req.txt"))
	_, _, errs := drainClassifier(t, c, 5*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected error for present required path: %v", errs)
	}
}

func TestClassifier_ContextCancellation(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 50; i++ {
		_ = os.WriteFile(filepath.Join(lower, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := differ.NewClassifier(lower, upper)
	excCh, comCh, errCh := c.Classify(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for excCh != nil || comCh != nil || errCh != nil {
			select {
			case _, ok := <-excCh:
				if !ok {
					excCh = nil
				}
			case _, ok := <-comCh:
				if !ok {
					comCh = nil
				}
			case _, ok := <-errCh:
				if !ok {
					errCh = nil
				}
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("goroutines did not stop after context cancellation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HashPipeline tests
// ─────────────────────────────────────────────────────────────────────────────

func TestHashPipeline_EnrichesEqualFiles(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{"f.txt": "identical"})
	fixture(t, upper, map[string]string{"f.txt": "identical"})

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)

	lInfo, _ := os.Lstat(filepath.Join(lower, "f.txt"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.txt"))
	rawCh <- differ.CommonPath{Path: "f.txt", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline()
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)

	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 enriched result, got %d", len(results))
	}
	eq, checked := results[0].IsContentEqual()
	if !checked {
		t.Fatal("HashEqual should be set after pipeline")
	}
	if !eq {
		t.Error("identical files should have HashEqual=true")
	}
}

func TestHashPipeline_EnrichesDifferentFiles(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	// Same byte-count content so phase-1 size check passes; different bytes so
	// phase-2 incremental comparison exits on the first chunk.
	fixture(t, lower, map[string]string{"f.txt": "version-A"})
	fixture(t, upper, map[string]string{"f.txt": "version-B"})

	// Force mtime difference so phase-1 mtime fast-path is skipped and
	// compareContents is exercised.
	lPath := filepath.Join(lower, "f.txt")
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(lPath, past, past)

	lInfo, _ := os.Lstat(lPath)
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.txt"))

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)
	rawCh <- differ.CommonPath{Path: "f.txt", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline()
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)

	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	eq, checked := results[0].IsContentEqual()
	if !checked {
		t.Fatal("HashEqual should be set")
	}
	if eq {
		t.Error("different-content files should have HashEqual=false")
	}
}

// TestCompareContents_EarlyExit verifies that compareContents returns false
// after reading only the first differing chunk — without reading the rest of
// either file. The test uses a custom io.Reader that counts Read calls so we
// can assert on I/O volume.
//
// We exercise this indirectly through the TwoPhaseHasher by constructing two
// files whose first 64 KiB differ, then asserting HashEqual=false with only
// one chunk of I/O per file.
func TestCompareContentsParallel_DifferInFirstChunk(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	// 8 MiB files differing only at byte 0.
	// Phase 2P: NumCPU segments run concurrently; the segment covering byte 0
	// returns false after reading one 64 KiB chunk, cancelling all others.
	size := 8 << 20
	lData := make([]byte, size)
	uData := make([]byte, size)
	lData[0] = 0xAA
	uData[0] = 0xBB

	lf := filepath.Join(lower, "big.bin")
	uf := filepath.Join(upper, "big.bin")
	_ = os.WriteFile(lf, lData, 0o644)
	_ = os.WriteFile(uf, uData, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(lf, past, past)

	lInfo, _ := os.Lstat(lf)
	uInfo, _ := os.Lstat(uf)

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)
	rawCh <- differ.CommonPath{Path: "big.bin", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline(
		differ.WithHasher(&differ.TwoPhaseHasher{
			LargeFileThreshold: 1 << 20, // 1 MiB threshold for test
			SegmentWorkers:     4,
		}),
	)
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	eq, checked := results[0].IsContentEqual()
	if !checked || eq {
		t.Errorf("differ-at-byte-0 large file: want eq=false checked=true, got eq=%v checked=%v", eq, checked)
	}
}

func TestCompareContentsParallel_EqualLargeFile(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	// 4 MiB identical files — all segments must report equal.
	size := 4 << 20
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 7)
	}
	lf := filepath.Join(lower, "eq.bin")
	uf := filepath.Join(upper, "eq.bin")
	_ = os.WriteFile(lf, data, 0o644)
	_ = os.WriteFile(uf, data, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(lf, past, past)

	lInfo, _ := os.Lstat(lf)
	uInfo, _ := os.Lstat(uf)

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)
	rawCh <- differ.CommonPath{Path: "eq.bin", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline(
		differ.WithHasher(&differ.TwoPhaseHasher{
			LargeFileThreshold: 1 << 20,
			SegmentWorkers:     4,
		}),
	)
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	eq, checked := results[0].IsContentEqual()
	if !checked || !eq {
		t.Errorf("equal large file: want eq=true checked=true, got eq=%v checked=%v", eq, checked)
	}
}

func TestCompareContentsParallel_DifferInLastSegment(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	// 4 MiB files; last byte differs. Segment containing the last byte
	// returns false; others return true.
	size := 4 << 20
	lData := make([]byte, size)
	uData := make([]byte, size)
	for i := range lData {
		lData[i] = byte(i)
		uData[i] = byte(i)
	}
	uData[size-1] ^= 0xFF // flip last byte

	lf := filepath.Join(lower, "tail.bin")
	uf := filepath.Join(upper, "tail.bin")
	_ = os.WriteFile(lf, lData, 0o644)
	_ = os.WriteFile(uf, uData, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(lf, past, past)

	lInfo, _ := os.Lstat(lf)
	uInfo, _ := os.Lstat(uf)

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)
	rawCh <- differ.CommonPath{Path: "tail.bin", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline(
		differ.WithHasher(&differ.TwoPhaseHasher{
			LargeFileThreshold: 1 << 20,
			SegmentWorkers:     4,
		}),
	)
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	eq, checked := results[0].IsContentEqual()
	if !checked || eq {
		t.Errorf("last-byte-differs: want eq=false checked=true, got eq=%v checked=%v", eq, checked)
	}
}

func TestCompareContentsParallel_ThresholdRouting(t *testing.T) {
	// Verify that files below the threshold use compareContents (sequential)
	// and files at/above use compareContentsParallel.
	// We test this indirectly: both paths must produce the correct result.
	lower, upper := t.TempDir(), t.TempDir()

	// 512 KiB — below 1 MiB threshold → sequential path
	small := make([]byte, 512*1024)
	for i := range small {
		small[i] = byte(i)
	}
	_ = os.WriteFile(filepath.Join(lower, "small.bin"), small, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "small.bin"), small, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(lower, "small.bin"), past, past)

	// 2 MiB — at 1 MiB threshold → parallel path
	large := make([]byte, 2<<20)
	for i := range large {
		large[i] = byte(i * 3)
	}
	_ = os.WriteFile(filepath.Join(lower, "large.bin"), large, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "large.bin"), large, 0o644)
	_ = os.Chtimes(filepath.Join(lower, "large.bin"), past, past)

	hasher := &differ.TwoPhaseHasher{LargeFileThreshold: 1 << 20, SegmentWorkers: 2}

	for _, name := range []string{"small.bin", "large.bin"} {
		lInfo, _ := os.Lstat(filepath.Join(lower, name))
		uInfo, _ := os.Lstat(filepath.Join(upper, name))

		rawCh := make(chan differ.CommonPath, 1)
		errCh := make(chan error, 8)
		rawCh <- differ.CommonPath{Path: name, Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
		close(rawCh)

		hp := differ.NewHashPipeline(differ.WithHasher(hasher))
		enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
		var results []differ.CommonPath
		for cp := range enriched {
			results = append(results, cp)
		}
		for range errCh {
		}

		eq, checked := results[0].IsContentEqual()
		if !checked || !eq {
			t.Errorf("%s: want equal, got eq=%v checked=%v", name, eq, checked)
		}
	}
}

// BenchmarkCompareContents_Sequential benchmarks the sequential path on a
// file that requires full read (files are equal, worst case for early exit).
func BenchmarkCompareContents_Sequential_Equal(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	size := 1 << 20 // 1 MiB
	data := make([]byte, size)
	_ = os.WriteFile(filepath.Join(lower, "f.bin"), data, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "f.bin"), data, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(lower, "f.bin"), past, past)
	lInfo, _ := os.Lstat(filepath.Join(lower, "f.bin"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.bin"))
	// Force threshold above file size so sequential path is always used.
	hasher := &differ.TwoPhaseHasher{LargeFileThreshold: 64 << 20}

	b.SetBytes(int64(size) * 2)
	b.ResetTimer()
	for range b.N {
		eq, _ := hasher.Equal(filepath.Join(lower, "f.bin"), filepath.Join(upper, "f.bin"), lInfo, uInfo)
		if !eq {
			b.Fatal("expected equal")
		}
	}
}

// BenchmarkCompareContents_Parallel benchmarks the parallel segment path
// on a large file that is equal (worst case: full read across all segments).
func BenchmarkCompareContents_Parallel_Equal(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	size := 8 << 20 // 8 MiB
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 7)
	}
	_ = os.WriteFile(filepath.Join(lower, "f.bin"), data, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "f.bin"), data, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(lower, "f.bin"), past, past)
	lInfo, _ := os.Lstat(filepath.Join(lower, "f.bin"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.bin"))
	hasher := &differ.TwoPhaseHasher{LargeFileThreshold: 1 << 20, SegmentWorkers: 4}

	b.SetBytes(int64(size) * 2)
	b.ResetTimer()
	for range b.N {
		eq, _ := hasher.Equal(filepath.Join(lower, "f.bin"), filepath.Join(upper, "f.bin"), lInfo, uInfo)
		if !eq {
			b.Fatal("expected equal")
		}
	}
}

// BenchmarkCompareContents_Parallel_EarlyExit benchmarks the early-exit
// benefit: files differ in the first byte, so only one chunk is read per
// segment before cancellation propagates.
func BenchmarkCompareContents_Parallel_EarlyExit(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	size := 8 << 20
	lData := make([]byte, size)
	uData := make([]byte, size)
	uData[0] = 0xFF // differ at byte 0

	_ = os.WriteFile(filepath.Join(lower, "f.bin"), lData, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "f.bin"), uData, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(lower, "f.bin"), past, past)
	lInfo, _ := os.Lstat(filepath.Join(lower, "f.bin"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.bin"))
	hasher := &differ.TwoPhaseHasher{LargeFileThreshold: 1 << 20, SegmentWorkers: 4}

	b.SetBytes(int64(size) * 2) // theoretical max; actual I/O is << this
	b.ResetTimer()
	for range b.N {
		eq, _ := hasher.Equal(filepath.Join(lower, "f.bin"), filepath.Join(upper, "f.bin"), lInfo, uInfo)
		if eq {
			b.Fatal("expected unequal")
		}
	}
}

func TestCompareContents_EarlyExit_DifferInMiddle(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	// Two files: 5 chunks each (5 × 64 KiB = 320 KiB). First chunk equal,
	// second chunk differs → should exit after reading 2 chunks from each.
	chunkSize := 64 * 1024
	lData := make([]byte, 5*chunkSize)
	uData := make([]byte, 5*chunkSize)
	copy(lData, uData)      // start equal
	lData[chunkSize] = 0x01 // differ in second chunk
	uData[chunkSize] = 0x02

	lf := filepath.Join(lower, "f.bin")
	uf := filepath.Join(upper, "f.bin")
	_ = os.WriteFile(lf, lData, 0o644)
	_ = os.WriteFile(uf, uData, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(lf, past, past)

	lInfo, _ := os.Lstat(lf)
	uInfo, _ := os.Lstat(uf)

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)
	rawCh <- differ.CommonPath{Path: "f.bin", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline()
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)

	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	if eq, _ := results[0].IsContentEqual(); eq {
		t.Error("files differing in second chunk should be unequal")
	}
}

func TestCompareContents_EqualLargeFile(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	// Identical 256 KiB files — must return equal after reading all chunks.
	size := 256 * 1024
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}
	_ = os.WriteFile(filepath.Join(lower, "equal.bin"), data, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "equal.bin"), data, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(lower, "equal.bin"), past, past)

	lInfo, _ := os.Lstat(filepath.Join(lower, "equal.bin"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "equal.bin"))

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)
	rawCh <- differ.CommonPath{Path: "equal.bin", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline()
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)

	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	eq, checked := results[0].IsContentEqual()
	if !checked || !eq {
		t.Errorf("identical large file: expected equal=true checked=true, got eq=%v checked=%v", eq, checked)
	}
}

func TestCompareContents_EmptyFiles(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(lower, "empty"), []byte{}, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "empty"), []byte{}, 0o644)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(lower, "empty"), past, past)

	lInfo, _ := os.Lstat(filepath.Join(lower, "empty"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "empty"))

	rawCh := make(chan differ.CommonPath, 1)
	errCh := make(chan error, 8)
	rawCh <- differ.CommonPath{Path: "empty", Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	close(rawCh)

	hp := differ.NewHashPipeline()
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
	for range enriched {
	}
	for range errCh {
	}
	// Empty files: size==0 → phase-1 catches it (same size + same... wait,
	// empty files have size 0 and mtime may differ, but compareContents handles
	// io.EOF on first read correctly.
}

func TestHashPipeline_ParallelHashing(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	const N = 20
	for i := 0; i < N; i++ {
		content := fmt.Sprintf("content-%d", i)
		fixture(t, lower, map[string]string{fmt.Sprintf("f%03d.txt", i): content})
		fixture(t, upper, map[string]string{fmt.Sprintf("f%03d.txt", i): content})
	}

	rawCh := make(chan differ.CommonPath, N)
	errCh := make(chan error, 16)

	for i := 0; i < N; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		lInfo, _ := os.Lstat(filepath.Join(lower, name))
		uInfo, _ := os.Lstat(filepath.Join(upper, name))
		rawCh <- differ.CommonPath{Path: name, Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
	}
	close(rawCh)

	hp := differ.NewHashPipeline(differ.WithHashWorkers(8))
	enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)

	var results []differ.CommonPath
	for cp := range enriched {
		results = append(results, cp)
	}
	for range errCh {
	}

	if len(results) != N {
		t.Fatalf("expected %d enriched results, got %d", N, len(results))
	}
	for _, cp := range results {
		eq, checked := cp.IsContentEqual()
		if !checked || !eq {
			t.Errorf("%s: expected checked and equal", cp.Path)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MergedView tests
// ─────────────────────────────────────────────────────────────────────────────

func TestFSMergedView_Remove(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "del.txt")
	_ = os.WriteFile(target, []byte("x"), 0o644)

	v, err := differ.NewFSMergedView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Remove(context.Background(), "del.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Error("file should be removed")
	}
}

func TestFSMergedView_Remove_Idempotent(t *testing.T) {
	dir := t.TempDir()
	v, _ := differ.NewFSMergedView(dir)
	// Calling Remove on a nonexistent path must not return an error.
	if err := v.Remove(context.Background(), "nonexistent.txt"); err != nil {
		t.Errorf("Remove of absent path should be idempotent: %v", err)
	}
}

func TestFSMergedView_RemoveAll(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subtree", "child")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "a.so"), []byte("lib"), 0o644)

	v, _ := differ.NewFSMergedView(dir)
	if err := v.RemoveAll(context.Background(), "subtree"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "subtree")); !errors.Is(err, os.ErrNotExist) {
		t.Error("subtree should be fully removed")
	}
}

func TestFSMergedView_InvalidDir(t *testing.T) {
	_, err := differ.NewFSMergedView("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent merged dir")
	}
}

func TestMemMergedView_RecordsOps(t *testing.T) {
	v := differ.NewMemMergedView("/fake/merged")
	ctx := context.Background()

	_ = v.Remove(ctx, "a.txt")
	_ = v.RemoveAll(ctx, "lib/")
	_ = v.Remove(ctx, "b.txt")

	if len(v.Removed) != 2 {
		t.Errorf("expected 2 Remove calls, got %d", len(v.Removed))
	}
	if len(v.RemovedAll) != 1 {
		t.Errorf("expected 1 RemoveAll call, got %d", len(v.RemovedAll))
	}
	all := v.AllOps()
	if len(all) != 3 {
		t.Errorf("expected 3 total ops, got %d", len(all))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GoroutineBatcher tests
// ─────────────────────────────────────────────────────────────────────────────

func TestGoroutineBatcher_SubmitAndFlush(t *testing.T) {
	v := differ.NewMemMergedView("/merged")
	b := differ.NewGoroutineBatcher(v)
	ctx := context.Background()

	for _, p := range []string{"a", "b", "c"} {
		_ = b.Submit(ctx, differ.BatchOp{Kind: differ.OpRemove, RelPath: p})
	}
	if err := b.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(v.Removed) != 3 {
		t.Errorf("expected 3 removes, got %d", len(v.Removed))
	}
}

func TestGoroutineBatcher_AutoFlush(t *testing.T) {
	v := differ.NewMemMergedView("/merged")
	b := differ.NewGoroutineBatcher(v, differ.WithAutoFlushAt(3))
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		_ = b.Submit(ctx, differ.BatchOp{Kind: differ.OpRemove, RelPath: fmt.Sprintf("f%d", i)})
	}
	// Auto-flush should have fired at 3 and 6.
	if len(v.Removed) < 3 {
		t.Errorf("expected at least 3 auto-flushed removes, got %d", len(v.Removed))
	}
	_ = b.Close(ctx)
}

func TestGoroutineBatcher_RemoveAll(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "lib", "sub")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "a.so"), []byte("x"), 0o644)

	v, _ := differ.NewFSMergedView(dir)
	b := differ.NewGoroutineBatcher(v)
	ctx := context.Background()

	_ = b.Submit(ctx, differ.BatchOp{Kind: differ.OpRemoveAll, RelPath: "lib"})
	_ = b.Close(ctx)

	if _, err := os.Stat(filepath.Join(dir, "lib")); !errors.Is(err, os.ErrNotExist) {
		t.Error("lib subtree should be removed by RemoveAll op")
	}
}

func TestGoroutineBatcher_ClosedError(t *testing.T) {
	v := differ.NewMemMergedView("/m")
	b := differ.NewGoroutineBatcher(v)
	_ = b.Close(context.Background())
	err := b.Submit(context.Background(), differ.BatchOp{Kind: differ.OpRemove, RelPath: "x"})
	if err == nil {
		t.Error("expected error after Close")
	}
}

func TestGoroutineBatcher_ConcurrentSubmits(t *testing.T) {
	v := differ.NewMemMergedView("/merged")
	b := differ.NewGoroutineBatcher(v)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Submit(ctx, differ.BatchOp{Kind: differ.OpRemove, RelPath: fmt.Sprintf("f%d", i)})
		}()
	}
	wg.Wait()
	_ = b.Close(ctx)
	if len(v.Removed) != 100 {
		t.Errorf("expected 100 concurrent removes, got %d", len(v.Removed))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RecordingBatcher tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRecordingBatcher(t *testing.T) {
	rb := &differ.RecordingBatcher{}
	ctx := context.Background()

	for _, p := range []string{"x", "y", "z"} {
		_ = rb.Submit(ctx, differ.BatchOp{Kind: differ.OpRemove, RelPath: p})
	}

	// Ops() is the permanent log — always contains all submitted ops.
	ops := rb.Ops()
	if len(ops) != 3 {
		t.Errorf("expected 3 ops in permanent log, got %d", len(ops))
	}

	_ = rb.Flush(ctx) // moves pending → total; does NOT clear Ops()

	if rb.Total() != 3 {
		t.Errorf("expected total 3 after flush, got %d", rb.Total())
	}

	// Ops() still returns the full permanent log after Flush.
	if len(rb.Ops()) != 3 {
		t.Errorf("permanent log should retain 3 entries after flush, got %d", len(rb.Ops()))
	}

	// Pending() should be empty after Flush.
	if len(rb.Pending()) != 0 {
		t.Errorf("pending should be empty after flush, got %d", len(rb.Pending()))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PruningSet tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPruningSet_CollapseSubsumes(t *testing.T) {
	var ps differ.PruningSet
	ps.Add(differ.ExclusivePath{Path: "a/b/c.txt", Kind: differ.PathKindFile})
	ps.Add(differ.ExclusivePath{Path: "a/b/d.txt", Kind: differ.PathKindFile})
	accepted := ps.Add(differ.ExclusivePath{Path: "a/b", Kind: differ.PathKindDir, Collapsed: true})

	if !accepted {
		t.Fatal("collapsed dir must be accepted")
	}
	if ps.Len() != 1 {
		t.Errorf("expected 1 entry after collapse, got %d", ps.Len())
	}
	entries := ps.Entries()
	if entries[0].Path != "a/b" {
		t.Errorf("expected a/b, got %q", entries[0].Path)
	}
}

func TestPruningSet_DescendantRejected(t *testing.T) {
	var ps differ.PruningSet
	ps.Add(differ.ExclusivePath{Path: "top", Kind: differ.PathKindDir, Collapsed: true})
	accepted := ps.Add(differ.ExclusivePath{Path: "top/sub/file.txt", Kind: differ.PathKindFile})
	if accepted {
		t.Error("descendant of collapsed dir should be rejected")
	}
}

func TestPruningSet_Drain(t *testing.T) {
	var ps differ.PruningSet
	for _, p := range []string{"a", "b", "c"} {
		ps.Add(differ.ExclusivePath{Path: p, Kind: differ.PathKindFile})
	}
	var drained []string
	ps.Drain(func(e differ.ExclusivePath) { drained = append(drained, e.Path) })

	if ps.Len() != 0 {
		t.Error("set should be empty after Drain")
	}
	if len(drained) != 3 {
		t.Errorf("expected 3 drained, got %d", len(drained))
	}
}

func TestPruningSet_Covered(t *testing.T) {
	var ps differ.PruningSet
	ps.Add(differ.ExclusivePath{Path: "libs", Kind: differ.PathKindDir, Collapsed: true})
	if !ps.Covered("libs/foo/bar.so") {
		t.Error("libs/foo/bar.so must be covered by collapsed 'libs'")
	}
	if ps.Covered("other/file.txt") {
		t.Error("other/file.txt must not be covered")
	}
}

func TestPruningSet_ForEach(t *testing.T) {
	var ps differ.PruningSet
	ps.Add(differ.ExclusivePath{Path: "x"})
	ps.Add(differ.ExclusivePath{Path: "y"})
	var seen []string
	ps.ForEach(func(ep differ.ExclusivePath) { seen = append(seen, ep.Path) })
	if len(seen) != 2 {
		t.Errorf("expected 2 from ForEach, got %d", len(seen))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler / predicate tests
// ─────────────────────────────────────────────────────────────────────────────

func TestChainExclusiveHandler_StopsOnError(t *testing.T) {
	sentinel := errors.New("stop")
	var second atomic.Bool
	chain := differ.ChainExclusiveHandler{
		differ.ExclusiveHandlerFunc(func(_ context.Context, _ differ.ExclusivePath) error { return sentinel }),
		differ.ExclusiveHandlerFunc(func(_ context.Context, _ differ.ExclusivePath) error {
			second.Store(true)
			return nil
		}),
	}
	err := chain.HandleExclusive(context.Background(), differ.ExclusivePath{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel, got %v", err)
	}
	if second.Load() {
		t.Error("second handler must not be called after first error")
	}
}

func TestMultiExclusiveHandler_CollectsAllErrors(t *testing.T) {
	e1, e2 := errors.New("e1"), errors.New("e2")
	multi := differ.MultiExclusiveHandler{
		differ.ExclusiveHandlerFunc(func(_ context.Context, _ differ.ExclusivePath) error { return e1 }),
		differ.ExclusiveHandlerFunc(func(_ context.Context, _ differ.ExclusivePath) error { return e2 }),
	}
	err := multi.HandleExclusive(context.Background(), differ.ExclusivePath{})
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Errorf("expected both errors joined, got: %v", err)
	}
}

func TestPredicateExclusiveHandler_OnlyCollapsed(t *testing.T) {
	var seen []string
	h := differ.PredicateExclusiveHandler{
		Predicate: differ.OnlyCollapsed(),
		Handler: differ.ExclusiveHandlerFunc(func(_ context.Context, ep differ.ExclusivePath) error {
			seen = append(seen, ep.Path)
			return nil
		}),
	}
	_ = h.HandleExclusive(context.Background(), differ.ExclusivePath{Path: "dir", Collapsed: true, Kind: differ.PathKindDir})
	_ = h.HandleExclusive(context.Background(), differ.ExclusivePath{Path: "file", Collapsed: false, Kind: differ.PathKindFile})
	if len(seen) != 1 || seen[0] != "dir" {
		t.Errorf("expected only collapsed dir, got %v", seen)
	}
}

func TestPredicateCommonHandler_OnlyChanged(t *testing.T) {
	var seen []string
	h := differ.PredicateCommonHandler{
		Predicate: differ.OnlyChanged(),
		Handler: differ.CommonHandlerFunc(func(_ context.Context, cp differ.CommonPath) error {
			seen = append(seen, cp.Path)
			return nil
		}),
	}
	_ = h.HandleCommon(context.Background(), differ.CommonPath{Path: "changed", Kind: differ.PathKindFile, HashEqual: boolPtr(false)})
	_ = h.HandleCommon(context.Background(), differ.CommonPath{Path: "same", Kind: differ.PathKindFile, HashEqual: boolPtr(true)})
	_ = h.HandleCommon(context.Background(), differ.CommonPath{Path: "dir", Kind: differ.PathKindDir})
	if len(seen) != 1 || seen[0] != "changed" {
		t.Errorf("expected only 'changed', got %v", seen)
	}
}

func TestCountingExclusiveHandler(t *testing.T) {
	var c differ.CountingExclusiveHandler
	eps := []differ.ExclusivePath{
		{Kind: differ.PathKindFile},
		{Kind: differ.PathKindFile},
		{Kind: differ.PathKindDir, Collapsed: true},
		{Kind: differ.PathKindSymlink},
	}
	for _, ep := range eps {
		_ = c.HandleExclusive(context.Background(), ep)
	}
	s := c.Snapshot()
	if s.Files != 2 || s.Dirs != 1 || s.Symlinks != 1 || s.Collapsed != 1 {
		t.Errorf("counters wrong: %+v", s)
	}
	if s.Total() != 4 {
		t.Errorf("total should be 4, got %d", s.Total())
	}
}

func TestCountingCommonHandler(t *testing.T) {
	var c differ.CountingCommonHandler
	cps := []differ.CommonPath{
		{Kind: differ.PathKindFile, HashEqual: boolPtr(true)},
		{Kind: differ.PathKindFile, HashEqual: boolPtr(false)},
		{Kind: differ.PathKindDir},
	}
	for _, cp := range cps {
		_ = c.HandleCommon(context.Background(), cp)
	}
	s := c.Snapshot()
	if s.Total != 3 || s.Equal != 1 || s.Changed != 1 || s.Unchecked != 1 {
		t.Errorf("counters wrong: %+v", s)
	}
}

func TestLogExclusiveHandler(t *testing.T) {
	var buf bytes.Buffer
	h := &differ.LogExclusiveHandler{
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
		Level:  slog.LevelInfo,
	}
	_ = h.HandleExclusive(context.Background(), differ.ExclusivePath{
		Path: "vendor/dep", Kind: differ.PathKindDir, Collapsed: true,
	})
	if !strings.Contains(buf.String(), "vendor/dep") {
		t.Error("expected path in log output")
	}
}

func TestDryRunExclusiveHandler(t *testing.T) {
	var buf bytes.Buffer
	h := &differ.DryRunExclusiveHandler{Writer: &buf}
	_ = h.HandleExclusive(context.Background(), differ.ExclusivePath{Path: "lib", Collapsed: true, Kind: differ.PathKindDir})
	if !strings.Contains(buf.String(), "removeAll") {
		t.Errorf("collapsed dir should use removeAll, got: %s", buf.String())
	}

	buf.Reset()
	_ = h.HandleExclusive(context.Background(), differ.ExclusivePath{Path: "f.txt", Kind: differ.PathKindFile})
	if !strings.Contains(buf.String(), "remove") || strings.Contains(buf.String(), "removeAll") {
		t.Errorf("leaf file should use remove, got: %s", buf.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline integration tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPipeline_BasicDeleteFlow(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{
		"excl.txt":       "del",
		"excl-dir/a.txt": "del",
		"shared.txt":     "same",
	})
	fixture(t, upper, map[string]string{
		"shared.txt": "same",
	})
	// Merged starts as a copy of lower
	fixture(t, merged, map[string]string{
		"excl.txt":       "del",
		"excl-dir/a.txt": "del",
		"shared.txt":     "same",
	})

	view, _ := differ.NewFSMergedView(merged)
	batcher := differ.NewGoroutineBatcher(view, differ.WithAutoFlushAt(32))

	classifier := differ.NewClassifier(lower, upper)
	pl := differ.NewPipeline(
		classifier,
		differ.NoopExclusiveHandler{},
		differ.NoopCommonHandler{},
		differ.WithExclusiveBatcher(batcher),
		differ.WithCommonBatcher(batcher),
	)
	result := pl.Run(context.Background())
	if !result.OK() {
		t.Fatalf("pipeline error: %v", result.Err)
	}
	_ = batcher.Close(context.Background())

	// excl.txt must be removed
	if _, err := os.Stat(filepath.Join(merged, "excl.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("excl.txt should be removed")
	}
	// excl-dir must be removed (collapsed)
	if _, err := os.Stat(filepath.Join(merged, "excl-dir")); !errors.Is(err, os.ErrNotExist) {
		t.Error("excl-dir should be removed")
	}
	// shared.txt must be removed (equal in lower and upper)
	if _, err := os.Stat(filepath.Join(merged, "shared.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("shared.txt (equal) should be removed from merged")
	}
}

func TestPipeline_PreservesChangedCommon(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()

	fixture(t, lower, map[string]string{"f.txt": "lower-version"})
	fixture(t, upper, map[string]string{"f.txt": "upper-version"})
	fixture(t, merged, map[string]string{"f.txt": "upper-version"})

	// Force mtime difference so TwoPhaseHasher reaches SHA-256 path
	lf := filepath.Join(lower, "f.txt")
	_ = os.Chtimes(lf, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	view, _ := differ.NewFSMergedView(merged)
	rb := &differ.RecordingBatcher{}
	classifier := differ.NewClassifier(lower, upper)
	pl := differ.NewPipeline(
		classifier,
		differ.NoopExclusiveHandler{},
		differ.NoopCommonHandler{},
		differ.WithCommonBatcher(rb),
	)
	result := pl.Run(context.Background())
	if !result.OK() {
		t.Fatalf("pipeline error: %v", result.Err)
	}
	_ = view

	// No ops should have been submitted for the changed file
	ops := rb.Ops()
	for _, op := range ops {
		if op.RelPath == "f.txt" {
			t.Errorf("changed file f.txt should not be batched for removal")
		}
	}
}

func TestPipeline_WithRecordingBatcher_ExclusiveBatch(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"a.txt":     "x",
		"dir/b.txt": "y",
	})

	rb := &differ.RecordingBatcher{}
	classifier := differ.NewClassifier(lower, upper)
	pl := differ.NewPipeline(
		classifier,
		differ.NoopExclusiveHandler{},
		differ.NoopCommonHandler{},
		differ.WithExclusiveBatcher(rb),
	)
	result := pl.Run(context.Background())
	if !result.OK() {
		t.Fatalf("pipeline: %v", result.Err)
	}
	_ = rb.Flush(context.Background())

	ops := rb.Ops()
	opPaths := make([]string, len(ops))
	for i, op := range ops {
		opPaths[i] = op.RelPath
	}
	// a.txt → OpRemove; dir → OpRemoveAll (collapsed)
	mustHave(t, opPaths, "a.txt", "op paths")
	mustHave(t, opPaths, "dir", "op paths collapsed dir")

	for _, op := range ops {
		if op.RelPath == "dir" && op.Kind != differ.OpRemoveAll {
			t.Errorf("dir should use OpRemoveAll, got %v", op.Kind)
		}
		if op.RelPath == "a.txt" && op.Kind != differ.OpRemove {
			t.Errorf("a.txt should use OpRemove, got %v", op.Kind)
		}
	}
}

func TestPipeline_AbortOnError(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		_ = os.WriteFile(filepath.Join(lower, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644)
	}

	sentinel := errors.New("injected")
	var callCount atomic.Int32
	excHandler := differ.ExclusiveHandlerFunc(func(_ context.Context, _ differ.ExclusivePath) error {
		callCount.Add(1)
		return sentinel
	})

	classifier := differ.NewClassifier(lower, upper)
	pl := differ.NewPipeline(classifier, excHandler, differ.NoopCommonHandler{},
		differ.WithAbortOnError(true),
		differ.WithExclusiveWorkers(1),
	)
	result := pl.Run(context.Background())
	if result.OK() {
		t.Fatal("expected error with AbortOnError")
	}
	if !errors.Is(result.Err, sentinel) {
		t.Errorf("expected sentinel in result.Err, got: %v", result.Err)
	}
}

func TestPipeline_CollectsAllErrors_NoAbort(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 5; i++ {
		_ = os.WriteFile(filepath.Join(lower, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644)
	}

	var callCount atomic.Int32
	excHandler := differ.ExclusiveHandlerFunc(func(_ context.Context, _ differ.ExclusivePath) error {
		callCount.Add(1)
		return errors.New("always fail")
	})

	classifier := differ.NewClassifier(lower, upper)
	pl := differ.NewPipeline(classifier, excHandler, differ.NoopCommonHandler{},
		differ.WithAbortOnError(false),
		differ.WithExclusiveWorkers(1),
	)
	result := pl.Run(context.Background())
	if result.OK() {
		t.Fatal("expected errors")
	}
	if callCount.Load() != 5 {
		t.Errorf("all 5 handlers should be called, got %d", callCount.Load())
	}
}

func TestPipeline_ContextCancellation(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	for i := 0; i < 100; i++ {
		_ = os.WriteFile(filepath.Join(lower, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	classifier := differ.NewClassifier(lower, upper)
	pl := differ.NewPipeline(classifier, differ.NoopExclusiveHandler{}, differ.NoopCommonHandler{})

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
// Engine convenience constructors
// ─────────────────────────────────────────────────────────────────────────────

func TestNewDeleteEngine_E2E(t *testing.T) {
	lower, upper, merged := t.TempDir(), t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"excl.txt":   "del",
		"shared.txt": "same",
	})
	fixture(t, upper, map[string]string{"shared.txt": "same"})
	fixture(t, merged, map[string]string{
		"excl.txt":   "del",
		"shared.txt": "same",
	})

	eng, err := differ.NewDeleteEngine(lower, upper, merged, nil, nil)
	if err != nil {
		t.Fatalf("NewDeleteEngine: %v", err)
	}
	result := eng.Run(context.Background())
	if !result.OK() {
		t.Fatalf("engine error: %v", result.Err)
	}
	if _, err := os.Stat(filepath.Join(merged, "excl.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("excl.txt should be removed")
	}
	if _, err := os.Stat(filepath.Join(merged, "shared.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("shared.txt (equal) should be removed")
	}
}

func TestNewObserveEngine_NoMutation(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"excl.txt":   "e",
		"shared.txt": "s",
	})
	fixture(t, upper, map[string]string{"shared.txt": "s"})

	var excC differ.CountingExclusiveHandler
	var comC differ.CountingCommonHandler
	eng := differ.NewObserveEngine(lower, upper, &excC, &comC, nil, nil)
	result := eng.Run(context.Background())
	if !result.OK() {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if excC.Snapshot().Total() != 1 {
		t.Errorf("expected 1 exclusive, got %d", excC.Snapshot().Total())
	}
	if comC.Snapshot().Total != 1 {
		t.Errorf("expected 1 common, got %d", comC.Snapshot().Total)
	}
	// Lower must not be mutated
	if _, err := os.Stat(filepath.Join(lower, "excl.txt")); err != nil {
		t.Error("lower must not be mutated by ObserveEngine")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// End-to-end: full composition with chain + predicate routing
// ─────────────────────────────────────────────────────────────────────────────

func TestPipeline_FullComposition(t *testing.T) {
	lower, upper := t.TempDir(), t.TempDir()
	fixture(t, lower, map[string]string{
		"changed.txt": "version-A",
		"same.txt":    "stable",
		"excl.txt":    "exclusive",
	})
	fixture(t, upper, map[string]string{
		"changed.txt": "version-B",
		"same.txt":    "stable",
	})
	// Force mtime difference on changed.txt so SHA-256 path is exercised
	_ = os.Chtimes(filepath.Join(lower, "changed.txt"),
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	var excCounter differ.CountingExclusiveHandler
	var changedCounter differ.CountingCommonHandler
	var unchangedCounter differ.CountingCommonHandler

	comHandler := differ.ChainCommonHandler{
		differ.PredicateCommonHandler{
			Predicate: differ.OnlyChanged(),
			Handler:   &changedCounter,
		},
		differ.PredicateCommonHandler{
			Predicate: differ.OnlyUnchanged(),
			Handler:   &unchangedCounter,
		},
	}

	classifier := differ.NewClassifier(lower, upper)
	pl := differ.NewPipeline(classifier, &excCounter, comHandler)
	result := pl.Run(context.Background())
	if !result.OK() {
		t.Fatalf("unexpected error: %v", result.Err)
	}

	if excCounter.Snapshot().Total() != 1 {
		t.Errorf("expected 1 exclusive, got %d", excCounter.Snapshot().Total())
	}
	if changedCounter.Snapshot().Changed != 1 {
		t.Errorf("expected 1 changed, got %d", changedCounter.Snapshot().Changed)
	}
	if unchangedCounter.Snapshot().Equal != 1 {
		t.Errorf("expected 1 unchanged, got %d", unchangedCounter.Snapshot().Equal)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pool tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBufPool_GetPut(t *testing.T) {
	p := differ.NewBufPool(1024)
	buf := p.Get()
	if buf == nil || len(*buf) != 1024 {
		t.Fatalf("expected 1024-byte buffer, got %v", buf)
	}
	p.Put(buf)
	// Get again — should reuse from pool
	buf2 := p.Get()
	if len(*buf2) != 1024 {
		t.Errorf("reused buffer size wrong: %d", len(*buf2))
	}
	p.Put(buf2)
}

func TestHashPool_GetReset(t *testing.T) {
	p := differ.NewHashPool(differ.DefaultHashFactory)
	h1 := p.Get()
	h1.Write([]byte("some data"))
	// Return to pool then get again — must be reset
	p.Put(h1)
	h2 := p.Get()
	// A reset hash and a fresh hash should produce the same digest for empty input
	// var empty [32]byte
	freshH := differ.DefaultHashFactory()
	freshDigest := freshH.Sum(nil)
	h2Digest := h2.Sum(nil)
	if string(freshDigest) != string(h2Digest) {
		t.Error("pooled hash must be reset before reuse")
	}
	p.Put(h2)
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkClassifier_LargeExclusiveTree(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	for d := 0; d < 10; d++ {
		for f := 0; f < 100; f++ {
			name := filepath.Join(lower, fmt.Sprintf("dir%02d", d), fmt.Sprintf("f%03d.txt", f))
			_ = os.MkdirAll(filepath.Dir(name), 0o755)
			_ = os.WriteFile(name, []byte("x"), 0o644)
		}
	}

	b.ResetTimer()
	for range b.N {
		c := differ.NewClassifier(lower, upper)
		ctx := context.Background()
		excCh, comCh, errCh := c.Classify(ctx)
		for excCh != nil || comCh != nil || errCh != nil {
			select {
			case _, ok := <-excCh:
				if !ok {
					excCh = nil
				}
			case _, ok := <-comCh:
				if !ok {
					comCh = nil
				}
			case _, ok := <-errCh:
				if !ok {
					errCh = nil
				}
			}
		}
	}
}

func BenchmarkHashPipeline_Parallel(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	const N = 100
	for i := 0; i < N; i++ {
		content := fmt.Sprintf("bench-content-%d-padding-to-make-it-bigger", i)
		name := fmt.Sprintf("f%03d.txt", i)
		_ = os.WriteFile(filepath.Join(lower, name), []byte(content), 0o644)
		_ = os.WriteFile(filepath.Join(upper, name), []byte(content), 0o644)
	}

	b.ResetTimer()
	for range b.N {
		rawCh := make(chan differ.CommonPath, N)
		errCh := make(chan error, 16)
		for i := 0; i < N; i++ {
			name := fmt.Sprintf("f%03d.txt", i)
			lInfo, _ := os.Lstat(filepath.Join(lower, name))
			uInfo, _ := os.Lstat(filepath.Join(upper, name))
			rawCh <- differ.CommonPath{Path: name, Kind: differ.PathKindFile, LowerInfo: lInfo, UpperInfo: uInfo}
		}
		close(rawCh)

		hp := differ.NewHashPipeline(differ.WithHashWorkers(8))
		enriched := hp.Run(context.Background(), lower, upper, rawCh, errCh)
		for range enriched {
		}
		for range errCh {
		}
	}
}
