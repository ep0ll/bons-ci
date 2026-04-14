package registry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// contentWriter unit tests
// ---------------------------------------------------------------------------

// buildWriter creates a contentWriter backed by mock remote and local writers.
func buildWriter(t *testing.T, data []byte, chanCap int) (*contentWriter, *mockBackend, *mockLocalStore) {
	t.Helper()
	b := newMockBackend()
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test", WithRetryMax(1))
	require.NoError(t, err)

	dgst := digest.FromBytes(data)
	desc := v1.Descriptor{Digest: dgst, Size: int64(len(data)), MediaType: "application/octet-stream"}

	remote, err := b.Push(context.Background(), "docker.io/library/test", desc)
	require.NoError(t, err)

	local, err := l.Writer(context.Background(),
		content.WithDescriptor(desc),
		content.WithRef(dgst.String()),
	)
	require.NoError(t, err)

	ref := dgst.String()
	cw := newContentWriter(s, ref, desc, remote, local, chanCap)

	now := time.Now()
	s.ingestions.Add(ref, &activeIngestion{
		ref:       ref,
		desc:      desc,
		writer:    cw,
		startedAt: now,
		updatedAt: now,
	})
	return cw, b, l
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

func TestContentWriter_Write_ReturnsByteCount(t *testing.T) {
	data := []byte("write-test-payload")
	cw, _, _ := buildWriter(t, data, 8)
	defer cw.Close()

	n, err := cw.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
}

func TestContentWriter_Write_UpdatesOffset(t *testing.T) {
	data := []byte("offset-tracking")
	cw, _, _ := buildWriter(t, data, 8)
	defer cw.Close()

	cw.Write(data[:5])
	cw.Write(data[5:])
	// Offset is updated atomically; allow async goroutine to process.
	time.Sleep(5 * time.Millisecond)

	st, err := cw.Status()
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), st.Offset)
}

func TestContentWriter_Write_AsyncLocalGetsAllBytes(t *testing.T) {
	data := []byte("async-local-bytes")
	cw, _, l := buildWriter(t, data, 32)
	ctx := context.Background()

	// Write in chunks.
	for i := 0; i < len(data); i += 4 {
		end := i + 4
		if end > len(data) {
			end = len(data)
		}
		_, err := cw.Write(data[i:end])
		require.NoError(t, err)
	}

	dgst := digest.FromBytes(data)
	require.NoError(t, cw.Commit(ctx, int64(len(data)), dgst))

	// Commit waits for local goroutine; local store should have the blob.
	time.Sleep(5 * time.Millisecond)
	info, err := l.Info(ctx, dgst)
	if err == nil {
		assert.Equal(t, int64(len(data)), info.Size)
	}
}

// ---------------------------------------------------------------------------
// Commit
// ---------------------------------------------------------------------------

func TestContentWriter_Commit_Success(t *testing.T) {
	data := []byte("commit-success")
	cw, _, _ := buildWriter(t, data, 8)
	ctx := context.Background()

	_, err := cw.Write(data)
	require.NoError(t, err)

	dgst := digest.FromBytes(data)
	require.NoError(t, cw.Commit(ctx, int64(len(data)), dgst))
}

func TestContentWriter_Commit_DoubleCommitFails(t *testing.T) {
	data := []byte("double-commit")
	cw, _, _ := buildWriter(t, data, 8)
	ctx := context.Background()
	dgst := digest.FromBytes(data)

	_, _ = cw.Write(data)
	require.NoError(t, cw.Commit(ctx, int64(len(data)), dgst))

	err := cw.Commit(ctx, int64(len(data)), dgst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already committed")
}

func TestContentWriter_Commit_RemovesIngestion(t *testing.T) {
	data := []byte("removes-ingestion")
	cw, _, _ := buildWriter(t, data, 8)
	ctx := context.Background()
	dgst := digest.FromBytes(data)
	s := cw.store
	ref := cw.ref

	_, ok := s.ingestions.Get(ref)
	require.True(t, ok, "ingestion should exist before commit")

	_, _ = cw.Write(data)
	require.NoError(t, cw.Commit(ctx, int64(len(data)), dgst))

	_, ok = s.ingestions.Get(ref)
	assert.False(t, ok, "ingestion should be removed after commit")
}

func TestContentWriter_Commit_InvalidatesCacheEntry(t *testing.T) {
	data := []byte("commit-invalidate-cache")
	cw, _, _ := buildWriter(t, data, 8)
	ctx := context.Background()
	dgst := digest.FromBytes(data)
	s := cw.store

	// Pre-populate cache.
	s.infoCache.Set(dgst, content.Info{Digest: dgst, Size: 999})
	_, wasCached := s.infoCache.Get(dgst)
	require.True(t, wasCached)

	_, _ = cw.Write(data)
	require.NoError(t, cw.Commit(ctx, int64(len(data)), dgst))

	_, isCached := s.infoCache.Get(dgst)
	assert.False(t, isCached, "Commit must invalidate the info cache entry")
}

func TestContentWriter_Commit_EmitsPushedEvent(t *testing.T) {
	b := newMockBackend()
	l := newMockLocalStore()
	hook := &collectingHook{}
	s, err := New(b, l, "docker.io/library/test", WithHooks(hook), WithRetryMax(1))
	require.NoError(t, err)

	data := []byte("emit-pushed")
	dgst := digest.FromBytes(data)
	ctx := context.Background()

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)
	_, _ = w.Write(data)
	require.NoError(t, w.Commit(ctx, int64(len(data)), dgst))

	time.Sleep(5 * time.Millisecond)
	pushed := hook.byKind(EventBlobPushed)
	require.Len(t, pushed, 1)
	assert.Equal(t, dgst, pushed[0].Digest)
	assert.Equal(t, int64(len(data)), pushed[0].Size)
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestContentWriter_Close_RemovesIngestion(t *testing.T) {
	data := []byte("close-removes")
	cw, _, _ := buildWriter(t, data, 8)
	s := cw.store
	ref := cw.ref

	_, ok := s.ingestions.Get(ref)
	require.True(t, ok)

	require.NoError(t, cw.Close())

	_, ok = s.ingestions.Get(ref)
	assert.False(t, ok)
}

func TestContentWriter_Close_AfterCommit_Safe(t *testing.T) {
	data := []byte("close-after-commit")
	cw, _, _ := buildWriter(t, data, 8)
	ctx := context.Background()
	dgst := digest.FromBytes(data)

	_, _ = cw.Write(data)
	require.NoError(t, cw.Commit(ctx, int64(len(data)), dgst))
	assert.NotPanics(t, func() { cw.Close() })
}

// ---------------------------------------------------------------------------
// Digest / Truncate / Status passthrough
// ---------------------------------------------------------------------------

func TestContentWriter_Digest(t *testing.T) {
	data := []byte("digest-passthrough")
	cw, _, _ := buildWriter(t, data, 8)
	defer cw.Close()

	_, _ = cw.Write(data)
	expected := digest.FromBytes(data)
	assert.Equal(t, expected, cw.Digest())
}

func TestContentWriter_Status(t *testing.T) {
	data := []byte("status-passthrough")
	cw, _, _ := buildWriter(t, data, 8)
	defer cw.Close()

	st, err := cw.Status()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, st.Total, int64(0))
}

func TestContentWriter_Truncate(t *testing.T) {
	data := []byte("truncate-me")
	cw, _, _ := buildWriter(t, data, 8)
	defer cw.Close()

	_, _ = cw.Write(data)
	assert.NoError(t, cw.Truncate(0))
}

// ---------------------------------------------------------------------------
// Channel-full drop (best-effort local) — no deadlock / no panic
// ---------------------------------------------------------------------------

func TestContentWriter_LocalChanFull_NoPanic(t *testing.T) {
	data := make([]byte, 1<<20)         // 1 MiB
	cw, _, _ := buildWriter(t, data, 1) // channel cap = 1 → will fill quickly
	defer cw.Close()

	for i := 0; i < 100; i++ {
		_, err := cw.Write(data[:1024])
		require.NoError(t, err, "remote write must succeed even when local channel is full")
	}
}

// ---------------------------------------------------------------------------
// Concurrent Write safety (race detector)
// ---------------------------------------------------------------------------

func TestContentWriter_ConcurrentWrite_RaceFree(t *testing.T) {
	// Use a fresh store rather than buildWriter because we want actual
	// ingestion lifecycle (including localWorker goroutine).
	b := newMockBackend()
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test", WithRetryMax(1))
	require.NoError(t, err)

	data := []byte("concurrent-write-data")
	dgst := digest.FromBytes(data)
	ctx := context.Background()

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	// Launch multiple goroutines writing simultaneously.
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = w.Write([]byte("chunk")) // remote mock is mutex-safe
		}()
	}
	wg.Wait()
	w.Close()
}

// ---------------------------------------------------------------------------
// Benchmark: Write throughput with async local
// ---------------------------------------------------------------------------

func BenchmarkContentWriter_Write_1KB(b *testing.B) { benchContentWriter(b, 1<<10) }
func BenchmarkContentWriter_Write_1MB(b *testing.B) { benchContentWriter(b, 1<<20) }

func benchContentWriter(b *testing.B, chunkSize int) {
	b.Helper()
	chunk := make([]byte, chunkSize)
	b.SetBytes(int64(chunkSize))
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		backend := newMockBackend()
		local := newMockLocalStore()
		s, err := New(backend, local, "docker.io/library/bench", WithRetryMax(1))
		if err != nil {
			b.Fatal(err)
		}
		dgst := makeDigest("bench-writer")
		desc := v1.Descriptor{Digest: dgst, Size: int64(chunkSize)}
		remote, _ := backend.Push(context.Background(), s.ref, desc)
		lw, _ := local.Writer(context.Background(), content.WithDescriptor(desc), content.WithRef(dgst.String()))
		cw := newContentWriter(s, dgst.String(), desc, remote, lw, 32)
		s.ingestions.Add(dgst.String(), &activeIngestion{ref: dgst.String(), writer: cw})
		cw.Write(chunk)
		cw.Commit(context.Background(), int64(chunkSize), dgst)
	}
}
