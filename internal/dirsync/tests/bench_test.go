package dirsync_test

// bench_test.go – benchmarks measuring walk throughput, fast-path efficiency,
// and hash pool scaling with multiple workers.
//
// Run with:
//
//	go test -run=^$ -bench=. -benchmem -benchtime=5s ./...
//
// Annotated output interpretation:
//
//	BenchmarkWalk_MetaEqualOnly   – pure merge-sort cost + metadata check
//	BenchmarkWalk_HashSlow        – adds incremental SHA-256 for every file
//	BenchmarkWalk_ExclusiveOnly   – pruning: emit dirs, no hashing
//	BenchmarkHashPool_Scaling     – worker count scaling for hash-heavy workloads

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
)

// ─── Filter-path benchmarks ───────────────────────────────────────────────────

// BenchmarkWalk_ExcludeFilter measures the overhead of an ExcludeFilter that
// prunes one large sub-tree from a 1 000-file tree.
//
// Compares NopFilter (baseline) vs ExcludeFilter ("dir_0000" pruned).
// The pruned directory contains 50 files that are never visited at all.
func BenchmarkWalk_ExcludeFilter(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	content := []byte("data")
	buildBenchTree(b, lower, 20, 50, content, fixedT) // 20 dirs × 50 files = 1 000
	buildBenchTree(b, upper, 20, 50, content, fixedT)

	for _, tc := range []struct {
		name string
		opts dirsync.Options
	}{
		{
			"NopFilter",
			dirsync.Options{HashWorkers: 4, ExclusiveBuf: 256, CommonBuf: 256},
		},
		{
			"ExcludeDir0000",
			dirsync.Options{
				ExcludePatterns: []string{"dir_0000"},
				HashWorkers:     4,
				ExclusiveBuf:    256,
				CommonBuf:       256,
			},
		},
	} {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				res, bErr := dirsync.Diff(context.Background(), lower, upper, tc.opts)
				if bErr != nil {
					b.Fatalf("Diff option error: %v", bErr)
				}
				if _, _, err := drainResult(res); err != nil {
					b.Fatalf("walk error: %v", err)
				}
			}
		})
	}
}

// BenchmarkWalk_IncludeFilter_GoFiles measures the cost of an IncludeFilter
// that keeps only *.go files from a mixed tree.
//
// The tree has 10 dirs × 20 files each; half are *.go, half are *.txt.
// emitLowerOnlyDir must be called for every exclusive dir whose name
// doesn't match *.go (all of them), then emit only the .go children.
func BenchmarkWalk_IncludeFilter_GoFiles(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	content := []byte("x")

	for i := 0; i < 10; i++ {
		dir := fmt.Sprintf("%s/pkg_%02d", lower, i)
		os.MkdirAll(dir, 0o755)
		for j := 0; j < 20; j++ {
			ext := ".go"
			if j%2 == 0 {
				ext = ".txt"
			}
			p := fmt.Sprintf("%s/file_%02d%s", dir, j, ext)
			os.WriteFile(p, content, 0o644)
			os.Chtimes(p, fixedT, fixedT)
		}
	}
	// upper is empty → all lower entries are exclusive.

	for _, tc := range []struct {
		name string
		opts dirsync.Options
	}{
		{
			"NopFilter_baseline",
			dirsync.Options{HashWorkers: 2, ExclusiveBuf: 256},
		},
		{
			"IncludeGoFiles",
			dirsync.Options{
				AllowWildcards:  true,
				IncludePatterns: []string{"*.go"},
				HashWorkers:     2,
				ExclusiveBuf:    256,
			},
		},
	} {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				res, bErr := dirsync.Diff(context.Background(), lower, upper, tc.opts)
				if bErr != nil {
					b.Fatalf("Diff option error: %v", bErr)
				}
				if _, _, err := drainResult(res); err != nil {
					b.Fatalf("walk error: %v", err)
				}
			}
		})
	}
}

// BenchmarkWalk_CompositeFilter measures the CompositeFilter path where
// ExcludePatterns prunes one sub-tree and IncludePatterns limits the rest.
func BenchmarkWalk_CompositeFilter(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	content := []byte("data")
	buildBenchTree(b, lower, 20, 50, content, fixedT)
	buildBenchTree(b, upper, 20, 50, content, fixedT)

	opts := dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.txt"},    // keep .txt files
		ExcludePatterns: []string{"dir_001*"}, // prune dirs matching dir_001x
		HashWorkers:     4,
		ExclusiveBuf:    256,
		CommonBuf:       256,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, bErr := dirsync.Diff(context.Background(), lower, upper, opts)
		if bErr != nil {
			b.Fatalf("Diff option error: %v", bErr)
		}
		if _, _, err := drainResult(res); err != nil {
			b.Fatalf("walk error: %v", err)
		}
	}
}


// buildBenchTree creates N files across M subdirectories under root.
// It returns the root path.  Files are written and their mtimes set uniformly
// so repeated bench iterations have consistent state.
//
// Layout:  root/dir_{i}/file_{j}.txt   for 0 ≤ i < dirs, 0 ≤ j < filesPerDir
func buildBenchTree(b *testing.B, root string, dirs, filesPerDir int, content []byte, mtime time.Time) {
	b.Helper()
	for i := 0; i < dirs; i++ {
		dir := fmt.Sprintf("%s/dir_%04d", root, i)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("mkdir %q: %v", dir, err)
		}
		for j := 0; j < filesPerDir; j++ {
			path := fmt.Sprintf("%s/file_%04d.txt", dir, j)
			if err := os.WriteFile(path, content, 0o644); err != nil {
				b.Fatalf("write %q: %v", path, err)
			}
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				b.Fatalf("chtimes %q: %v", path, err)
			}
		}
	}
}

// drainResult drains all channels returned by Diff, blocking until complete.
func drainResult(res dirsync.Result) (exclusive, common int, err error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range res.Exclusive {
			mu.Lock()
			exclusive++
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for range res.Common {
			mu.Lock()
			common++
			mu.Unlock()
		}
	}()
	wg.Wait()
	err = <-res.Err
	return
}

// ─── Benchmarks ───────────────────────────────────────────────────────────────

// BenchmarkWalk_MetaEqualOnly measures the best-case path: both trees are
// identical (same mtime, same size) so no content hashing is performed.
// This isolates the merge-sort walk and metadata comparison cost.
func BenchmarkWalk_MetaEqualOnly(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	content := []byte("benchmark file content – 40 bytes!!!!")
	buildBenchTree(b, lower, 20, 50, content, fixedT) // 1 000 files
	buildBenchTree(b, upper, 20, 50, content, fixedT)

	opts := dirsync.Options{HashWorkers: 4, ExclusiveBuf: 256, CommonBuf: 256}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, bErr := dirsync.Diff(context.Background(), lower, upper, opts)
		if bErr != nil { b.Fatalf("Diff option error: %v", bErr) }
		exc, com, err := drainResult(res)
		if err != nil {
			b.Fatalf("walk error: %v", err)
		}
		if exc != 0 {
			b.Fatalf("unexpected exclusive paths: %d", exc)
		}
		_ = com
	}
}

// BenchmarkWalk_HashSlow measures the worst-case common path: every file has a
// different mtime forcing SHA-256 content hashing for all of them.
// This saturates the hash pool and measures end-to-end throughput.
func BenchmarkWalk_HashSlow(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	content := []byte("benchmark content for hashing – needs to be long enough to exercise io.CopyBuffer")
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second) // force hash path for every file

	buildBenchTree(b, lower, 10, 20, content, t1) // 200 files
	buildBenchTree(b, upper, 10, 20, content, t2)

	opts := dirsync.Options{HashWorkers: 4, ExclusiveBuf: 64, CommonBuf: 256}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, bErr := dirsync.Diff(context.Background(), lower, upper, opts)
		if bErr != nil { b.Fatalf("Diff option error: %v", bErr) }
		_, _, err := drainResult(res)
		if err != nil {
			b.Fatalf("walk error: %v", err)
		}
	}
}

// BenchmarkWalk_ExclusiveOnly measures the pruning path: no upper entries
// exist, so every lower directory is emitted as a pruned root.
// Tests emission throughput without any hashing.
func BenchmarkWalk_ExclusiveOnly(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	content := []byte("exclusive")
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	buildBenchTree(b, lower, 50, 20, content, t1) // 50 pruned dirs emitted

	opts := dirsync.Options{HashWorkers: 2, ExclusiveBuf: 256, CommonBuf: 64}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, bErr := dirsync.Diff(context.Background(), lower, upper, opts)
		if bErr != nil { b.Fatalf("Diff option error: %v", bErr) }
		exc, _, err := drainResult(res)
		if err != nil {
			b.Fatalf("walk error: %v", err)
		}
		if exc != 50 {
			b.Fatalf("expected 50 pruned dirs, got %d", exc)
		}
	}
}

// BenchmarkHashPool_Scaling benchmarks hash pool performance as worker count
// increases from 1 to GOMAXPROCS.  Useful for tuning the HashWorkers option.
func BenchmarkHashPool_Scaling(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	content := make([]byte, 64*1024) // 64 KiB per file – one full buffer read
	for i := range content {
		content[i] = byte(i % 251)
	}

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	buildBenchTree(b, lower, 4, 25, content, t1) // 100 × 64 KiB = 6.4 MiB
	buildBenchTree(b, upper, 4, 25, content, t2)

	for _, workers := range []int{1, 2, 4, 8} {
		workers := workers
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			opts := dirsync.Options{
				HashWorkers:  workers,
				ExclusiveBuf: 64,
				CommonBuf:    256,
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				res, bErr := dirsync.Diff(context.Background(), lower, upper, opts)
				if bErr != nil {
					b.Fatalf("Diff option error: %v", bErr)
				}
				_, _, err := drainResult(res)
				if err != nil {
					b.Fatalf("walk error: %v", err)
				}
			}
		})
	}
}

// BenchmarkWalk_LargeFlat measures a flat directory with thousands of entries
// (common for layer roots in container images).  Tests merge-sort at scale.
func BenchmarkWalk_LargeFlat(b *testing.B) {
	lower := b.TempDir()
	upper := b.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	content := []byte("flat file")

	// 2 000 shared files + 500 exclusive.
	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("shared_%06d.txt", i)
		for _, root := range []string{lower, upper} {
			p := root + "/" + name
			os.WriteFile(p, content, 0o644)
			os.Chtimes(p, fixedT, fixedT)
		}
	}
	for i := 0; i < 500; i++ {
		name := fmt.Sprintf("excl_%06d.txt", i)
		p := lower + "/" + name
		os.WriteFile(p, content, 0o644)
		os.Chtimes(p, fixedT, fixedT)
	}

	opts := dirsync.Options{HashWorkers: 4, ExclusiveBuf: 1024, CommonBuf: 1024}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, bErr := dirsync.Diff(context.Background(), lower, upper, opts)
		if bErr != nil { b.Fatalf("Diff option error: %v", bErr) }
		exc, com, err := drainResult(res)
		if err != nil {
			b.Fatalf("walk error: %v", err)
		}
		if exc != 500 {
			b.Fatalf("exclusive = %d, want 500", exc)
		}
		if com < 2000 {
			b.Fatalf("common = %d, want ≥2000", com)
		}
	}
}
