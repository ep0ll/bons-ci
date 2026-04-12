package registry

import (
	"context"
	"fmt"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// benchStore creates a fresh Store for benchmarks (retryMax=1 avoids sleep overhead).
func benchStore(b *testing.B) (*Store, *mockBackend, *mockLocalStore) {
	b.Helper()
	backend := newMockBackend()
	local := newMockLocalStore()
	s, err := New(backend, local, "docker.io/library/bench", WithRetryMax(1))
	require.NoError(b, err)
	return s, backend, local
}

// seedLocalB seeds a blob into the local store for benchmarks.
func seedLocalB(b *testing.B, l *mockLocalStore, data []byte) digest.Digest {
	b.Helper()
	dgst := digest.FromBytes(data)
	l.commit(dgst, data)
	return dgst
}

// ---------------------------------------------------------------------------
// Info benchmarks
// ---------------------------------------------------------------------------

// BenchmarkStore_Info_CacheHit measures the sharded InfoCache hot path.
// This is the most important benchmark: it exercises FNV-1a shard selection,
// RLock, map lookup, and hook fan-out, all with zero allocations on hit.
func BenchmarkStore_Info_CacheHit(b *testing.B) {
	s, _, l := benchStore(b)
	dgst := seedLocalB(b, l, make([]byte, 1<<20))
	// Warm the cache.
	_, err := s.Info(context.Background(), dgst)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Info(context.Background(), dgst)
		}
	})
}

// BenchmarkStore_Info_LocalStore skips the in-memory cache (always goes to local store).
func BenchmarkStore_Info_LocalStore(b *testing.B) {
	_, _, l := benchStore(b)
	dgst := seedLocalB(b, l, make([]byte, 1<<20))
	// Fresh store so no cache is seeded.
	s, err := New(newMockBackend(), l, "docker.io/library/bench",
		WithCacheTTL(0), // zero TTL means every Get is expired
		WithRetryMax(1),
	)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.Info(context.Background(), dgst)
	}
}

// BenchmarkStore_Info_Parallel_256Digests stresses the shard distribution with
// 256 distinct digests to expose false-sharing or hash-collision hot-spots.
func BenchmarkStore_Info_Parallel_256Digests(b *testing.B) {
	s, _, l := benchStore(b)
	digests := make([]digest.Digest, 256)
	for i := range digests {
		data := []byte(fmt.Sprintf("digest-%d", i))
		digests[i] = seedLocalB(b, l, data)
		_, _ = s.Info(context.Background(), digests[i]) // warm
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		k := 0
		for pb.Next() {
			s.Info(context.Background(), digests[k%256])
			k++
		}
	})
}

func BenchmarkStore_ReaderAt_LocalHit_1KB(b *testing.B)  { benchReaderAt(b, 1<<10) }
func BenchmarkStore_ReaderAt_LocalHit_64KB(b *testing.B) { benchReaderAt(b, 64<<10) }
func BenchmarkStore_ReaderAt_LocalHit_1MB(b *testing.B)  { benchReaderAt(b, 1<<20) }
func BenchmarkStore_ReaderAt_LocalHit_16MB(b *testing.B) { benchReaderAt(b, 16<<20) }

func benchReaderAt(b *testing.B, size int) {
	b.Helper()
	s, _, l := benchStore(b)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}
	dgst := seedLocalB(b, l, data)
	desc := v1.Descriptor{Digest: dgst, Size: int64(size)}
	readBuf := make([]byte, minInt(size, 32<<10))

	b.SetBytes(int64(size))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ra, err := s.ReaderAt(context.Background(), desc)
		if err != nil {
			b.Fatal(err)
		}
		ra.ReadAt(readBuf, 0)
		ra.Close()
	}
}

// ---------------------------------------------------------------------------
// Writer benchmarks
// ---------------------------------------------------------------------------

func BenchmarkStore_Writer_Lifecycle_1KB(b *testing.B)  { benchWriter(b, 1<<10) }
func BenchmarkStore_Writer_Lifecycle_64KB(b *testing.B) { benchWriter(b, 64<<10) }
func BenchmarkStore_Writer_Lifecycle_1MB(b *testing.B)  { benchWriter(b, 1<<20) }

func benchWriter(b *testing.B, size int) {
	b.Helper()
	data := make([]byte, size)
	b.SetBytes(int64(size))
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		s, _, _ := benchStore(b)
		dgst := makeDigest(fmt.Sprintf("w-%d", i))
		w, err := s.Writer(context.Background(), content.WithRef(dgst.String()))
		if err != nil {
			b.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			b.Fatal(err)
		}
		if err = w.Commit(context.Background(), int64(size), dgst); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStore_Writer_Concurrent measures throughput with concurrent writers
// each using their own unique ref (no duplicate-ingestion contention).
func BenchmarkStore_Writer_Concurrent(b *testing.B) {
	data := make([]byte, 4<<10) // 4 KiB per writer
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			s, _, _ := benchStore(b)
			dgst := makeDigest(fmt.Sprintf("par-%d", i))
			w, err := s.Writer(context.Background(), content.WithRef(dgst.String()))
			if err != nil {
				i++
				continue
			}
			w.Write(data)
			w.Commit(context.Background(), int64(len(data)), dgst)
			i++
		}
	})
}

func BenchmarkWriter_Write_LocalAsync_1KB(b *testing.B)  { benchWriterAsync(b, 1<<10) }
func BenchmarkWriter_Write_LocalAsync_1MB(b *testing.B)  { benchWriterAsync(b, 1<<20) }

func benchWriterAsync(b *testing.B, chunkSize int) {
	b.Helper()
	data := make([]byte, chunkSize)
	b.SetBytes(int64(chunkSize))
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		backend := newMockBackend()
		local := newMockLocalStore()
		s, err := New(backend, local, "docker.io/library/bench", WithRetryMax(1))
		if err != nil { b.Fatal(err) }
		dgst := makeDigest(fmt.Sprintf("async-%d", i))
		w, err := s.Writer(context.Background(), content.WithRef(dgst.String()))
		if err != nil {
			b.Fatal(err)
		}
		w.Write(data)
		w.Commit(context.Background(), int64(chunkSize), dgst)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Hook fan-out benchmarks
// ---------------------------------------------------------------------------

func BenchmarkEmit_NoHooks(b *testing.B) {
	s, _, _ := benchStore(b)
	evt := Event{Kind: EventBlobCached, Digest: makeDigest("bench")}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.emit(context.Background(), evt)
		}
	})
}

func BenchmarkEmit_OneHook(b *testing.B) {
	hook := HookFunc(func(_ context.Context, _ Event) {})
	s, err := New(newMockBackend(), newMockLocalStore(), "docker.io/library/bench",
		WithHooks(hook), WithRetryMax(1))
	require.NoError(b, err)
	evt := Event{Kind: EventBlobCached, Digest: makeDigest("bench")}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.emit(context.Background(), evt)
		}
	})
}

func BenchmarkEmit_EightHooks(b *testing.B) {
	hooks := make([]Hook, 8)
	for i := range hooks {
		hooks[i] = HookFunc(func(_ context.Context, _ Event) {})
	}
	s, err := New(newMockBackend(), newMockLocalStore(), "docker.io/library/bench",
		WithHooks(hooks...), WithRetryMax(1))
	require.NoError(b, err)
	evt := Event{Kind: EventBlobCached, Digest: makeDigest("bench")}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.emit(context.Background(), evt)
	}
}
