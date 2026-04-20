//go:build linux

// bench/bench_test.go – empirical performance benchmarks.
//
// Run all benchmarks:
//
//	go test ./bench/ -bench=. -benchmem -benchtime=5s -count=3
//
// Run a single group:
//
//	go test ./bench/ -bench=BenchmarkStrategy    -benchmem -benchtime=5s
//	go test ./bench/ -bench=BenchmarkParallelWorkers -benchmem -benchtime=5s
//	go test ./bench/ -bench=BenchmarkChunkSize   -benchmem -benchtime=10s
//	go test ./bench/ -bench=BenchmarkHashDB      -benchmem -benchtime=5s
//
// The 4 MiB and 8 MiB sizes straddle the default sequential/mmap threshold
// (8 MiB) and are the most important for tuning container image layer hashing,
// where shared library files cluster between 1 MiB and 16 MiB.
package bench_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/bons/bons-ci/plugins/checksummer/engine"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/cache"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hashdb"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hasher"
)

// ─────────────────────────── fixtures ────────────────────────────────────────

// fileMatrix is the canonical size matrix used across all benchmarks.
// 4 MiB and 8 MiB are explicitly included because they straddle the
// sequential/mmap strategy boundary and dominate container layer workloads.
var fileMatrix = []struct {
	name string
	size int64
}{
	{"64KiB", 64 << 10},
	{"1MiB", 1 << 20},
	{"4MiB", 4 << 20}, // below default 8 MiB threshold → sequential
	{"8MiB", 8 << 20}, // at default threshold → first mmap candidate
	{"16MiB", 16 << 20},
	{"32MiB", 32 << 20},
	{"64MiB", 64 << 20},
	{"128MiB", 128 << 20},
	{"512MiB", 512 << 20},
}

// tempFile creates a random-content temp file of exactly size bytes.
func tempFile(b *testing.B, size int64) *os.File {
	b.Helper()
	f, err := os.CreateTemp("", "bench-*.bin")
	if err != nil {
		b.Fatalf("CreateTemp: %v", err)
	}
	b.Cleanup(func() { os.Remove(f.Name()) })

	const chunk = 4 << 20
	buf := make([]byte, chunk)
	for written := int64(0); written < size; {
		n := min64(int64(chunk), size-written)
		if _, err := rand.Read(buf[:n]); err != nil {
			b.Fatalf("rand.Read: %v", err)
		}
		if _, err := f.Write(buf[:n]); err != nil {
			b.Fatalf("write: %v", err)
		}
		written += n
	}
	if _, err := f.Seek(0, 0); err != nil {
		b.Fatalf("seek: %v", err)
	}
	return f
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ─────────────────────────── BenchmarkStrategy ────────────────────────────────
// Compares Sequential vs Mmap vs Parallel at every file size.
// The 4 MiB and 8 MiB rows are critical: they show where each strategy wins.

func BenchmarkStrategy(b *testing.B) {
	ctx := context.Background()

	strategies := []struct {
		name string
		h    hasher.Hasher
	}{
		{"sequential", hasher.NewBlake3Hasher(0)},
		{"mmap", hasher.NewMmapHasher()},
		{"parallel/w4", hasher.NewParallelHasher(hasher.WithWorkers(4))},
		{"parallel/w8", hasher.NewParallelHasher(hasher.WithWorkers(8))},
	}

	for _, tc := range fileMatrix {
		for _, st := range strategies {
			tc, st := tc, st
			b.Run(tc.name+"/"+st.name, func(b *testing.B) {
				f := tempFile(b, tc.size)
				b.SetBytes(tc.size)
				b.ReportMetric(float64(tc.size>>20), "MiB/file")
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := st.h.HashFile(ctx, f.Name()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// ─────────────────────────── BenchmarkAdaptive ────────────────────────────────
// Adaptive auto-selects the strategy per file size.
// Reports the chosen strategy as a custom metric so you can verify selection.

func BenchmarkAdaptive(b *testing.B) {
	h := hasher.NewAdaptiveHasher()
	ctx := context.Background()

	for _, tc := range fileMatrix {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			f := tempFile(b, tc.size)
			strat := h.Strategy(tc.size)
			b.ReportMetric(float64(strat), "strategy(0=seq,1=mmap,2=par)")
			b.SetBytes(tc.size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := h.HashFile(ctx, f.Name()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ─────────────────────────── BenchmarkAdaptiveFD ─────────────────────────────
// Hash via open fd (fanotify path) vs reopening by path.
// FD path avoids one open()+close() syscall pair per hash.

func BenchmarkAdaptiveFD(b *testing.B) {
	h := hasher.NewAdaptiveHasher()
	ctx := context.Background()

	for _, tc := range fileMatrix {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			f := tempFile(b, tc.size)
			b.SetBytes(tc.size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := h.HashFD(ctx, int(f.Fd()), tc.size); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ─────────────────────────── BenchmarkParallelWorkers ────────────────────────
// Worker-count scaling at 4 MiB and 8 MiB (the critical boundary sizes).
// Confirms that parallel workers help (or hurt) at these specific sizes.

func BenchmarkParallelWorkers(b *testing.B) {
	ctx := context.Background()
	workerCounts := []int{1, 2, 4, 8, runtime.NumCPU()}

	// Focus on 4 MiB and 8 MiB where the choice of strategy matters most.
	sizes := []struct {
		name string
		size int64
	}{
		{"4MiB", 4 << 20},
		{"8MiB", 8 << 20},
		{"32MiB", 32 << 20},
		{"128MiB", 128 << 20},
	}

	for _, tc := range sizes {
		for _, w := range workerCounts {
			tc, w := tc, w
			b.Run(fmt.Sprintf("%s/w%d", tc.name, w), func(b *testing.B) {
				h := hasher.NewParallelHasher(
					hasher.WithWorkers(w),
					hasher.WithChunkSize(hasher.DefaultChunkSize),
				)
				f := tempFile(b, tc.size)
				b.SetBytes(tc.size)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := h.HashFile(ctx, f.Name()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// ─────────────────────────── BenchmarkChunkSize ───────────────────────────────
// Chunk-size tuning at 4 MiB, 8 MiB, and 128 MiB.
// Smaller chunks → more goroutines → more queue-depth pressure.
// Larger chunks → fewer goroutines → less overhead, but less parallelism.

func BenchmarkChunkSize(b *testing.B) {
	ctx := context.Background()

	chunkSizes := []int64{
		256 << 10, // 256 KiB
		512 << 10, // 512 KiB
		1 << 20,   // 1 MiB
		2 << 20,   // 2 MiB  (default)
		4 << 20,   // 4 MiB
		8 << 20,   // 8 MiB
	}

	sizes := []struct {
		name string
		size int64
	}{
		{"4MiB", 4 << 20},
		{"8MiB", 8 << 20},
		{"128MiB", 128 << 20},
	}

	for _, tc := range sizes {
		for _, cs := range chunkSizes {
			tc, cs := tc, cs
			b.Run(fmt.Sprintf("%s/chunk=%s", tc.name, fmtBytes(cs)), func(b *testing.B) {
				h := hasher.NewParallelHasher(
					hasher.WithWorkers(8),
					hasher.WithChunkSize(cs),
				)
				f := tempFile(b, tc.size)
				b.SetBytes(tc.size)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := h.HashFile(ctx, f.Name()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// ─────────────────────────── BenchmarkHashBytes ───────────────────────────────
// In-memory BLAKE3 throughput (no I/O overhead).

func BenchmarkHashBytes(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"4KiB", 4 << 10},
		{"64KiB", 64 << 10},
		{"1MiB", 1 << 20},
		{"4MiB", 4 << 20},
		{"8MiB", 8 << 20},
		{"16MiB", 16 << 20},
	}
	for _, tc := range sizes {
		tc := tc
		data := make([]byte, tc.size)
		_, _ = rand.Read(data)
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(tc.size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = hasher.HashBytes(data)
			}
		})
	}
}

// ─────────────────────────── BenchmarkHashReaderAt ───────────────────────────
// Parallel io.ReaderAt path — the "guaranteed improvement" path for any
// source that implements ReaderAt (os.File, bytes.Reader, HTTP ranges).

func BenchmarkHashReaderAt(b *testing.B) {
	ctx := context.Background()
	ph := hasher.NewParallelHasher(hasher.WithWorkers(8))

	sizes := []struct {
		name string
		size int64
	}{
		{"4MiB", 4 << 20},
		{"8MiB", 8 << 20},
		{"32MiB", 32 << 20},
		{"128MiB", 128 << 20},
	}

	for _, tc := range sizes {
		tc := tc
		data := make([]byte, tc.size)
		_, _ = rand.Read(data)
		r := bytes.NewReader(data)

		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(tc.size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ph.HashReaderAt(ctx, r, 0, tc.size); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ─────────────────────────── BenchmarkBufferPool ─────────────────────────────
// Measures the pool hot path vs raw allocation.

func BenchmarkBufferPool(b *testing.B) {
	pool := hasher.NewBufferPool()
	sizes := []int{4 << 10, 64 << 10, 1 << 20, 4 << 20, 8 << 20}

	for _, sz := range sizes {
		sz := sz
		b.Run(fmtBytes(int64(sz)), func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					buf := pool.GetAtLeast(sz)
					pool.Put(buf)
				}
			})
		})
	}

	b.Run("vs-alloc/pool-2MiB", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				buf := pool.GetAtLeast(2 << 20)
				pool.Put(buf)
			}
		})
	})
	b.Run("vs-alloc/alloc-2MiB", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				buf := make([]byte, 2<<20)
				_ = buf
			}
		})
	})
}

// ─────────────────────────── BenchmarkHashDB ─────────────────────────────────
// Measures the persistent hash database get/put/flush performance.
// This is the L3 cache latency that is added to the dedup path when enabled.

func BenchmarkHashDB(b *testing.B) {
	makeKey := func(i int) filekey.Key {
		k := filekey.Key{Source: filekey.SourceHandle, MountID: 42}
		k.Handle[0] = byte(i)
		k.Handle[1] = byte(i >> 8)
		k.Handle[2] = byte(i >> 16)
		k.Handle[3] = 0x42
		k.HandleLen = 4
		return k
	}
	hash32 := make([]byte, 32)
	_, _ = rand.Read(hash32)

	b.Run("Put", func(b *testing.B) {
		dir := b.TempDir()
		db, _ := hashdb.Open(dir)
		defer db.Close()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			db.Put(makeKey(i), int64(i)*1e9, int64(i)*4096, hash32)
		}
	})

	b.Run("Get-hit", func(b *testing.B) {
		dir := b.TempDir()
		db, _ := hashdb.Open(dir)
		defer db.Close()
		// Pre-populate 1000 entries.
		for i := 0; i < 1000; i++ {
			db.Put(makeKey(i), int64(i)*1e9, int64(i)*4096, hash32)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			idx := i % 1000
			_, _ = db.Get(makeKey(idx), int64(idx)*1e9, int64(idx)*4096)
		}
	})

	b.Run("Get-miss", func(b *testing.B) {
		dir := b.TempDir()
		db, _ := hashdb.Open(dir)
		defer db.Close()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = db.Get(makeKey(i+100000), 999, 999) // never populated
		}
	})

	b.Run("Flush-1000entries", func(b *testing.B) {
		dir := b.TempDir()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			db, _ := hashdb.Open(dir)
			for j := 0; j < 1000; j++ {
				db.Put(makeKey(j), int64(j)*1e9, int64(j)*4096, hash32)
			}
			b.StartTimer()
			_ = db.Flush()
			db.Close()
			// Remove shard files between iterations to keep flush cost consistent.
			_ = os.RemoveAll(dir)
			_ = os.MkdirAll(dir, 0755)
		}
	})

	b.Run("OpenLoad-256shards", func(b *testing.B) {
		// Measure DB open (shard load + compaction) after persisting 10k entries.
		dir := b.TempDir()
		db0, _ := hashdb.Open(dir)
		for i := 0; i < 10000; i++ {
			db0.Put(makeKey(i), int64(i)*1e9, 4096, hash32)
		}
		db0.Close()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			db, _ := hashdb.Open(dir)
			db.Close()
		}
	})
}

// ─────────────────────────── BenchmarkDedupCacheLayers ────────────────────────
// Measures the overhead of each dedup layer:
//   L2: in-process LRU cache hit (fastest path)
//   L3: persistent hashdb hit (disk → memory, then L2 promotion)

func BenchmarkDedupCacheLayers(b *testing.B) {
	f := tempFile(b, 4<<20)
	ctx := context.Background()

	b.Run("L2-cache-hit", func(b *testing.B) {
		eng, _ := engine.Build().CacheMaxEntries(1024).Engine()
		_, _ = eng.HashPath(ctx, f.Name()) // prime L2
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = eng.HashPath(ctx, f.Name())
		}
	})

	b.Run("L3-hashdb-hit", func(b *testing.B) {
		dir := b.TempDir()
		// Prime L3 with a fresh engine.
		eng1, _ := engine.Build().HashDBDir(dir).Engine()
		_, _ = eng1.HashPath(ctx, f.Name())
		_ = eng1.FlushDB()

		// New engine: empty L2, populated L3.
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			eng2, _ := engine.Build().HashDBDir(dir).Engine()
			b.StartTimer()
			_, _ = eng2.HashPath(ctx, f.Name()) // L3 hit → L2 promotion
		}
	})
}

// ─────────────────────────── BenchmarkFileKey ────────────────────────────────
// Sub-microsecond operations on the hot key-resolution path.

func BenchmarkFileKey(b *testing.B) {
	f := tempFile(b, 4096)
	r := filekey.DefaultResolver

	b.Run("FromFD", func(b *testing.B) {
		fd := int(f.Fd())
		for i := 0; i < b.N; i++ {
			_, _ = r.FromFD(fd)
		}
	})

	b.Run("FromPath", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = r.FromPath(f.Name())
		}
	})

	b.Run("SFKey-no-alloc", func(b *testing.B) {
		k, _ := r.FromPath(f.Name())
		for i := 0; i < b.N; i++ {
			_ = k.SFKey()
		}
	})

	b.Run("Hash-no-alloc", func(b *testing.B) {
		k, _ := r.FromPath(f.Name())
		for i := 0; i < b.N; i++ {
			_ = k.Hash()
		}
	})
}

// ─────────────────────────── BenchmarkCache ──────────────────────────────────

func BenchmarkCache(b *testing.B) {
	c := cache.New(cache.WithMaxEntries(4096))
	hash := make([]byte, 32)
	_, _ = rand.Read(hash)

	b.Run("Set", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				c.Set("bench-key", hash)
			}
		})
	})

	b.Run("Get-hit", func(b *testing.B) {
		c.Set("bench-key", hash)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _ = c.Get("bench-key")
			}
		})
	})

	b.Run("Get-miss", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _ = c.Get("no-such-key-xyzzy")
			}
		})
	})
}

// ─────────────────────────── BenchmarkSystemReport ───────────────────────────

func BenchmarkSystemReport(b *testing.B) {
	b.Logf("CPU: GOMAXPROCS=%d NumCPU=%d GOOS=%s GOARCH=%s",
		runtime.GOMAXPROCS(0), runtime.NumCPU(), runtime.GOOS, runtime.GOARCH)
	b.Logf("Recommended: --workers=%d --parallel-workers=%d",
		runtime.NumCPU()*2, runtime.NumCPU())
	b.Logf("4MiB threshold: below → sequential pread64")
	b.Logf("8MiB threshold: at boundary → first mmap candidate")
	b.Logf("128MiB threshold: above → parallel NVMe multi-queue")
}

// ─────────────────────────── helpers ─────────────────────────────────────────

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%dGiB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%dMiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dKiB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
