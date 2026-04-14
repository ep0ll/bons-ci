package registry

import (
	"context"
	"fmt"
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
// Test factory
// ---------------------------------------------------------------------------

func testStore(t *testing.T) (*Store, *mockBackend, *mockLocalStore) {
	t.Helper()
	b := newMockBackend()
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test", WithRetryMax(1))
	require.NoError(t, err)
	return s, b, l
}

// ---------------------------------------------------------------------------
// New — constructor validation
// ---------------------------------------------------------------------------

func TestNew_NilBackend(t *testing.T) {
	_, err := New(nil, newMockLocalStore(), "docker.io/library/test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend must not be nil")
}

func TestNew_NilLocalStore(t *testing.T) {
	_, err := New(newMockBackend(), nil, "docker.io/library/test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local store must not be nil")
}

func TestNew_EmptyRef(t *testing.T) {
	_, err := New(newMockBackend(), newMockLocalStore(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestNew_InvalidRef(t *testing.T) {
	_, err := New(newMockBackend(), newMockLocalStore(), "INVALID:::REF")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference")
}

func TestNew_AllOptions(t *testing.T) {
	hook := HookFunc(func(_ context.Context, _ Event) {})
	s, err := New(newMockBackend(), newMockLocalStore(), "docker.io/library/test",
		WithHooks(hook),
		WithCacheTTL(10*time.Minute),
		WithRetryMax(2),
		WithWriterWorkerLimit(8),
	)
	require.NoError(t, err)
	assert.Len(t, s.hooks, 1)
	assert.Equal(t, 2, s.retryMax)
	assert.Equal(t, 8, s.workerLimit)
}

// ---------------------------------------------------------------------------
// Info
// ---------------------------------------------------------------------------

func TestStore_Info_EmptyDigest(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Info(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty digest")
	var se *StoreError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, ErrKindInvalidArgument, se.Kind)
}

func TestStore_Info_LocalHit(t *testing.T) {
	s, _, l := testStore(t)
	data := []byte("local-info")
	dgst := seedLocal(t, l, data)

	info, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
	assert.Equal(t, int64(len(data)), info.Size)
}

func TestStore_Info_RemoteFallback(t *testing.T) {
	s, b, _ := testStore(t)
	data := []byte("remote-info")
	dgst := seedRemote(t, b, data)

	info, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
	assert.Equal(t, int64(len(data)), info.Size)
}

func TestStore_Info_CacheHit_BypassesRemote(t *testing.T) {
	s, b, _ := testStore(t)
	data := []byte("cache-hit")
	dgst := seedRemote(t, b, data)

	// Warm the cache.
	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	// Remove from backend; second call must succeed from cache alone.
	b.mu.Lock()
	delete(b.blobs, dgst)
	delete(b.descs, dgst)
	b.mu.Unlock()

	info, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
}

func TestStore_Info_NotFound(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Info(context.Background(), makeDigest("missing"))
	require.Error(t, err)
}

func TestStore_Info_CacheTTLExpiry(t *testing.T) {
	b := newMockBackend()
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test",
		WithCacheTTL(10*time.Millisecond),
		WithRetryMax(1),
	)
	require.NoError(t, err)

	dgst := seedRemote(t, b, []byte("ttl-data"))
	_, err = s.Info(context.Background(), dgst)
	require.NoError(t, err)

	time.Sleep(25 * time.Millisecond)

	// Remove from backend; cache expired so remote will be hit (and fail).
	b.mu.Lock()
	delete(b.blobs, dgst)
	delete(b.descs, dgst)
	b.mu.Unlock()

	_, err = s.Info(context.Background(), dgst)
	require.Error(t, err, "should fail after TTL expiry when backend is empty")
}

// ---------------------------------------------------------------------------
// ReaderAt
// ---------------------------------------------------------------------------

func TestStore_ReaderAt_InvalidDigest(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: "invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid digest")
}

func TestStore_ReaderAt_LocalHit(t *testing.T) {
	s, _, l := testStore(t)
	data := []byte("reader-local")
	dgst := seedLocal(t, l, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	defer ra.Close()

	buf := make([]byte, len(data))
	n, err := ra.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, data, buf[:n])
	assert.Equal(t, int64(len(data)), ra.Size())
}

func TestStore_ReaderAt_RemoteFetch(t *testing.T) {
	s, b, _ := testStore(t)
	data := []byte("reader-remote")
	dgst := seedRemote(t, b, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	defer ra.Close()

	buf := make([]byte, len(data))
	n, err := ra.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, data, buf[:n])
}

func TestStore_ReaderAt_FetchError(t *testing.T) {
	s, b, _ := testStore(t)
	b.fetchErr = fmt.Errorf("network error")
	dgst := makeDigest("fail")

	_, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: 4})
	require.Error(t, err)

	var se *StoreError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, ErrKindUnavailable, se.Kind)
}

// ---------------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------------

func TestStore_WriterLifecycle_WriteCommit(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	data := []byte("writer-lifecycle")
	dgst := digest.FromBytes(data)

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	n, err := w.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)

	require.NoError(t, w.Commit(ctx, int64(len(data)), dgst))
}

func TestStore_Writer_DuplicateIngestion(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := makeDigest("dup")

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	_, err = s.Writer(ctx, content.WithRef(dgst.String()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already active")

	var se *StoreError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, ErrKindAlreadyExists, se.Kind)
}

func TestStore_Writer_NoRefOrDigest(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Writer(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestStore_Writer_DigestFromRef(t *testing.T) {
	s, _, _ := testStore(t)
	dgst := makeDigest("ref-digest")
	w, err := s.Writer(context.Background(), content.WithRef("test@"+dgst.String()))
	require.NoError(t, err)
	require.NotNil(t, w)
	w.Close()
}

func TestStore_Writer_DoubleCommit(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	data := []byte("double-commit")
	dgst := digest.FromBytes(data)

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)
	_, _ = w.Write(data)
	require.NoError(t, w.Commit(ctx, int64(len(data)), dgst))

	err = w.Commit(ctx, int64(len(data)), dgst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already committed")
}

func TestStore_Writer_CloseReleasesIngestion(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := makeDigest("close-release")

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// After close the same ref must be available again.
	w2, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)
	w2.Close()
}

func TestStore_Writer_PushError(t *testing.T) {
	b := newMockBackend()
	b.pushErr = fmt.Errorf("push refused")
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test", WithRetryMax(1))
	require.NoError(t, err)

	dgst := makeDigest("push-fail")
	_, err = s.Writer(context.Background(), content.WithRef(dgst.String()))
	require.Error(t, err)

	var se *StoreError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, ErrKindUnavailable, se.Kind)
}

func TestStore_Writer_StatusDuringIngestion(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := makeDigest("status")

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	st, err := s.Status(ctx, dgst.String())
	require.NoError(t, err)
	assert.Equal(t, dgst.String(), st.Ref)
	assert.False(t, st.StartedAt.IsZero())
}

// ---------------------------------------------------------------------------
// Abort
// ---------------------------------------------------------------------------

func TestStore_Abort_ExistingIngestion(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := makeDigest("abort")

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	require.NoError(t, s.Abort(ctx, dgst.String()))

	// After abort the ref must be available again.
	w2, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)
	w2.Close()
}

func TestStore_Abort_Unknown(t *testing.T) {
	s, _, _ := testStore(t)
	err := s.Abort(context.Background(), "nonexistent")
	require.Error(t, err) // falls through to local store which also returns error
}

// ---------------------------------------------------------------------------
// Status / ListStatuses
// ---------------------------------------------------------------------------

func TestStore_ListStatuses_Empty(t *testing.T) {
	s, _, _ := testStore(t)
	ss, err := s.ListStatuses(context.Background())
	require.NoError(t, err)
	assert.Empty(t, ss)
}

func TestStore_ListStatuses_WithActiveIngestion(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := makeDigest("list-status")

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	ss, err := s.ListStatuses(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, ss)
}

func TestStore_ListStatuses_Filter(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()

	for _, name := range []string{"alpha-1", "alpha-2", "beta-1"} {
		dgst := makeDigest(name)
		_, err := s.Writer(ctx, content.WithRef(name+"@"+dgst.String()))
		require.NoError(t, err)
	}

	ss, err := s.ListStatuses(ctx, "alpha")
	require.NoError(t, err)
	assert.Len(t, ss, 2, "filter 'alpha' should match alpha-1 and alpha-2")
}

// ---------------------------------------------------------------------------
// Delete / Update / Walk
// ---------------------------------------------------------------------------

func TestStore_Delete(t *testing.T) {
	s, _, l := testStore(t)
	dgst := seedLocal(t, l, []byte("delete-me"))

	require.NoError(t, s.Delete(context.Background(), dgst))
	_, err := s.Info(context.Background(), dgst)
	require.Error(t, err)
}

func TestStore_Delete_InvalidatesCacheEntry(t *testing.T) {
	s, b, _ := testStore(t)
	dgst := seedRemote(t, b, []byte("cache-invalidate"))

	// Populate cache.
	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	// Seed local so Delete succeeds.
	s.local.(*mockLocalStore).commit(dgst, []byte("cache-invalidate"))
	require.NoError(t, s.Delete(context.Background(), dgst))

	// Cache must be invalidated.
	_, cacheHit := s.infoCache.Get(dgst)
	assert.False(t, cacheHit)
}

func TestStore_Delete_NotFound(t *testing.T) {
	s, _, _ := testStore(t)
	err := s.Delete(context.Background(), makeDigest("missing"))
	require.Error(t, err)
}

func TestStore_Update(t *testing.T) {
	s, _, l := testStore(t)
	dgst := seedLocal(t, l, []byte("update-me"))

	updated, err := s.Update(context.Background(), content.Info{
		Digest: dgst,
		Labels: map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, "test", updated.Labels["env"])
}

func TestStore_Update_InvalidatesCacheEntry(t *testing.T) {
	s, _, l := testStore(t)
	dgst := seedLocal(t, l, []byte("upd-cache"))

	// Populate cache.
	_, _ = s.Info(context.Background(), dgst)
	_, wasCached := s.infoCache.Get(dgst)
	assert.True(t, wasCached)

	_, _ = s.Update(context.Background(), content.Info{Digest: dgst})

	_, stillCached := s.infoCache.Get(dgst)
	assert.False(t, stillCached, "Update must invalidate the cache entry")
}

func TestStore_Walk_VisitsAllBlobs(t *testing.T) {
	s, _, l := testStore(t)
	for _, payload := range [][]byte{[]byte("walk-a"), []byte("walk-b"), []byte("walk-c")} {
		seedLocal(t, l, payload)
	}

	var visited []digest.Digest
	require.NoError(t, s.Walk(context.Background(), func(info content.Info) error {
		visited = append(visited, info.Digest)
		return nil
	}))
	assert.Len(t, visited, 3)
}

func TestStore_Walk_EarlyExit(t *testing.T) {
	s, _, l := testStore(t)
	seedLocal(t, l, []byte("ea"))
	seedLocal(t, l, []byte("eb"))

	count := 0
	err := s.Walk(context.Background(), func(content.Info) error {
		count++
		return fmt.Errorf("stop")
	})
	require.Error(t, err)
	assert.Equal(t, 1, count)
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestStore_Close_CancelsActiveIngestions(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		dgst := makeDigest(fmt.Sprintf("close-%d", i))
		_, err := s.Writer(ctx, content.WithRef(dgst.String()))
		require.NoError(t, err)
	}

	require.NoError(t, s.Close())
}

func TestStore_Close_FlushesInfoCache(t *testing.T) {
	s, b, _ := testStore(t)
	dgst := seedRemote(t, b, []byte("flush-on-close"))
	_, _ = s.Info(context.Background(), dgst)

	_, wasCached := s.infoCache.Get(dgst)
	require.True(t, wasCached)

	require.NoError(t, s.Close())

	_, isCached := s.infoCache.Get(dgst)
	assert.False(t, isCached, "Close must flush the info cache")
}

// ---------------------------------------------------------------------------
// digestFromRef (unit tests for the parsing helper)
// ---------------------------------------------------------------------------

func TestDigestFromRef_Table(t *testing.T) {
	validDigest := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	cases := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{"bare_digest", validDigest, false},
		{"name_at_digest", "test@" + validDigest, false},
		{"empty", "", true},
		{"no_at_no_digest", "justname", true},
		{"bad_digest", "test@notadigest", true},
		{"at_bad_digest", "name@sha256:tooshort", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := digestFromRef(tc.ref)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// matchesFilters
// ---------------------------------------------------------------------------

func TestMatchesFilters_EmptyMatchesAll(t *testing.T) {
	st := content.Status{Ref: "anything"}
	assert.True(t, matchesFilters(st, nil))
	assert.True(t, matchesFilters(st, []string{}))
}

func TestMatchesFilters_PrefixMatch(t *testing.T) {
	st := content.Status{Ref: "docker.io/library/nginx"}
	assert.True(t, matchesFilters(st, []string{"docker.io"}))
	assert.True(t, matchesFilters(st, []string{"docker.io/library"}))
	assert.False(t, matchesFilters(st, []string{"quay.io"}))
}

// ---------------------------------------------------------------------------
// Concurrent stress tests (run with -race)
// ---------------------------------------------------------------------------

func TestStore_ConcurrentInfo_RaceFree(t *testing.T) {
	s, b, _ := testStore(t)
	dgst := seedRemote(t, b, []byte("concurrent-info"))

	const goroutines = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, errs[i] = s.Info(context.Background(), dgst)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d", i)
	}
}

func TestStore_ConcurrentWriter_RaceFree(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	const writers = 50

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			data := []byte(fmt.Sprintf("concurrent-writer-%d", i))
			dgst := digest.FromBytes(data)
			ref := fmt.Sprintf("ref-%d@", i) + dgst.String()
			w, err := s.Writer(ctx, content.WithRef(ref))
			if err != nil {
				return
			}
			w.Write(data)
			w.Commit(ctx, int64(len(data)), dgst)
		}()
	}
	wg.Wait()
}

func TestStore_ConcurrentMixedOps_RaceFree(t *testing.T) {
	s, b, l := testStore(t)
	ctx := context.Background()

	// Pre-seed some blobs.
	digests := make([]digest.Digest, 20)
	for i := range digests {
		data := []byte(fmt.Sprintf("mixed-%d", i))
		if i%2 == 0 {
			digests[i] = seedLocal(t, l, data)
		} else {
			digests[i] = seedRemote(t, b, data)
		}
	}

	var wg sync.WaitGroup
	const goroutines = 100
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			dgst := digests[i%len(digests)]
			switch i % 3 {
			case 0:
				s.Info(ctx, dgst)
			case 1:
				desc := v1.Descriptor{Digest: dgst, Size: int64(i % 64)}
				ra, err := s.ReaderAt(ctx, desc)
				if err == nil {
					ra.Close()
				}
			case 2:
				s.ListStatuses(ctx)
			}
		}()
	}
	wg.Wait()
}
