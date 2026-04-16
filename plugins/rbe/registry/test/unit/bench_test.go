// Package unit contains benchmarks for all performance-critical paths.
//
// Run with:
//
//	go test ./test/unit/ -bench=. -benchmem -benchtime=3s
//
// Key benchmarks:
//   - Bloom filter: Add, Test, AddDigestString, TestDigestString
//   - ShardedIndex: concurrent Index, Query hot path, ExistsAny
//   - ContentStore: Put, Get, concurrent Get
package unit

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	digest "github.com/opencontainers/go-digest"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/index"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/storage/memory"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/bloom"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Bloom filter benchmarks
// ────────────────────────────────────────────────────────────────────────────

var benchFilter = bloom.NewDefault(1_000_000)

func init() {
	// Pre-populate filter for Test benchmarks
	for i := 0; i < 500_000; i++ {
		benchFilter.AddString(fmt.Sprintf("sha256:element-%d", i))
	}
}

func BenchmarkBloom_AddString(b *testing.B) {
	f := bloom.New(bloom.DefaultM, bloom.DefaultK)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f.AddString(fmt.Sprintf("sha256:bench-%d", i))
	}
}

func BenchmarkBloom_TestString_Hit(b *testing.B) {
	key := "sha256:element-12345"
	benchFilter.AddString(key)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = benchFilter.TestString(key)
	}
}

func BenchmarkBloom_TestString_Miss(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = benchFilter.TestString(fmt.Sprintf("sha256:miss-%d", i))
	}
}

func BenchmarkBloom_AddDigestString(b *testing.B) {
	f := bloom.NewDefault(1_000_000)
	dgst := digest.Canonical.FromString("bench-image:latest")
	s := dgst.String()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f.AddDigestString(s)
	}
}

func BenchmarkBloom_TestDigestString_Hit(b *testing.B) {
	f := bloom.NewDefault(1_000_000)
	dgst := digest.Canonical.FromString("bench-image:latest")
	s := dgst.String()
	f.AddDigestString(s)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = f.TestDigestString(s)
	}
}

func BenchmarkBloom_Concurrent_Add(b *testing.B) {
	f := bloom.NewDefault(10_000_000)
	var counter int64
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := atomic.AddInt64(&counter, 1)
			f.AddDigestString(fmt.Sprintf("sha256:parallel-%d", i))
		}
	})
}

// ────────────────────────────────────────────────────────────────────────────
// ShardedIndex benchmarks
// ────────────────────────────────────────────────────────────────────────────

var (
	benchIdx     = index.NewShardedIndex(1_000_000)
	benchSources = func() []digest.Digest {
		srcs := make([]digest.Digest, 10_000)
		for i := range srcs {
			srcs[i] = digest.Canonical.FromString(fmt.Sprintf("bench-source-%d", i))
		}
		return srcs
	}()
)

func init() {
	ctx := context.Background()
	for _, src := range benchSources {
		v := makeVariant(types.AccelNydus, src)
		_ = benchIdx.Index(ctx, v)
	}
}

func BenchmarkIndex_Index(b *testing.B) {
	ctx := context.Background()
	idx := index.NewShardedIndex(100_000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		src := digest.Canonical.FromString(fmt.Sprintf("bench-idx-source-%d", i))
		v := makeVariant(types.AccelNydus, src)
		_ = idx.Index(ctx, v)
	}
}

func BenchmarkIndex_Query_Hit(b *testing.B) {
	ctx := context.Background()
	src := benchSources[42]
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result, _ := benchIdx.Query(ctx, src)
		_ = result.Found
	}
}

func BenchmarkIndex_Query_Miss(b *testing.B) {
	ctx := context.Background()
	absent := digest.Canonical.FromString("definitely-not-in-index")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result, _ := benchIdx.Query(ctx, absent)
		_ = result.Found
	}
}

func BenchmarkIndex_ExistsAny_Hit(b *testing.B) {
	ctx := context.Background()
	src := benchSources[100]
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = benchIdx.ExistsAny(ctx, src)
	}
}

func BenchmarkIndex_ExistsAny_Miss(b *testing.B) {
	ctx := context.Background()
	absent := digest.Canonical.FromString("absent-for-existsany")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = benchIdx.ExistsAny(ctx, absent)
	}
}

func BenchmarkIndex_Concurrent_Index(b *testing.B) {
	idx := index.NewShardedIndex(10_000_000)
	ctx := context.Background()
	var counter int64
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := atomic.AddInt64(&counter, 1)
			src := digest.Canonical.FromString(fmt.Sprintf("parallel-src-%d", i))
			v := makeVariant(types.AccelEstargz, src)
			_ = idx.Index(ctx, v)
		}
	})
}

func BenchmarkIndex_Concurrent_Query(b *testing.B) {
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			src := benchSources[i%len(benchSources)]
			i++
			result, _ := benchIdx.Query(ctx, src)
			_ = result.Found
		}
	})
}

// ────────────────────────────────────────────────────────────────────────────
// ContentStore benchmarks
// ────────────────────────────────────────────────────────────────────────────

var benchStore = memory.New()

func init() {
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		data := []byte(fmt.Sprintf("bench-blob-data-%d", i))
		dgst := digest.Canonical.FromBytes(data)
		_ = benchStore.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))
	}
}

func BenchmarkStore_Put_Small(b *testing.B) {
	ctx := context.Background()
	s := memory.New()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		data := []byte(fmt.Sprintf("small-blob-%d", i))
		dgst := digest.Canonical.FromBytes(data)
		_ = s.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))
	}
}

func BenchmarkStore_Put_Large(b *testing.B) {
	ctx := context.Background()
	s := memory.New()
	data := make([]byte, 1024*1024) // 1 MiB
	for i := range data {
		data[i] = byte(i % 256)
	}
	dgst := digest.Canonical.FromBytes(data)
	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		_ = s.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))
	}
}

func BenchmarkStore_Get(b *testing.B) {
	ctx := context.Background()
	s := memory.New()
	data := []byte("get-benchmark-blob")
	dgst := digest.Canonical.FromBytes(data)
	_ = s.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rc, err := s.Get(ctx, dgst)
		if err == nil {
			rc.Close()
		}
	}
}

func BenchmarkStore_Exists(b *testing.B) {
	ctx := context.Background()
	data := []byte("exists-benchmark-blob")
	dgst := digest.Canonical.FromBytes(data)
	_ = benchStore.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = benchStore.Exists(ctx, dgst)
	}
}

func BenchmarkStore_Concurrent_Get(b *testing.B) {
	ctx := context.Background()
	data := []byte("concurrent-get-blob")
	dgst := digest.Canonical.FromBytes(data)
	_ = benchStore.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rc, err := benchStore.Get(ctx, dgst)
			if err == nil {
				rc.Close()
			}
		}
	})
}
