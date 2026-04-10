package registry

import (
	"context"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testStore(t *testing.T) (*Store, *mockBackend, *mockLocalStore) {
	t.Helper()
	backend := newMockBackend()
	local := newMockLocalStore()
	store, err := New(backend, local, "docker.io/library/test")
	require.NoError(t, err)
	return store, backend, local
}

func seedRemote(t *testing.T, backend *mockBackend, data []byte) digest.Digest {
	t.Helper()
	return backend.seed(data)
}

func seedLocal(t *testing.T, local *mockLocalStore, data []byte) digest.Digest {
	t.Helper()
	dgst := digest.FromBytes(data)
	local.commit(dgst, data)
	return dgst
}

// ---------------------------------------------------------------------------
// New — constructor validation
// ---------------------------------------------------------------------------

func TestNew_NilBackend(t *testing.T) {
	_, err := New(nil, newMockLocalStore(), "docker.io/test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend must not be nil")
}

func TestNew_NilLocalStore(t *testing.T) {
	_, err := New(newMockBackend(), nil, "docker.io/test")
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

// ---------------------------------------------------------------------------
// Info
// ---------------------------------------------------------------------------

func TestStore_Info_LocalHit(t *testing.T) {
	s, _, local := testStore(t)
	data := []byte("local-data")
	dgst := seedLocal(t, local, data)

	info, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
	assert.Equal(t, int64(len(data)), info.Size)
}

func TestStore_Info_RemoteFallback(t *testing.T) {
	s, backend, _ := testStore(t)
	data := []byte("remote-data")
	dgst := seedRemote(t, backend, data)

	info, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
	assert.Equal(t, int64(len(data)), info.Size)
}

func TestStore_Info_CacheHit(t *testing.T) {
	s, backend, _ := testStore(t)
	data := []byte("cached-data")
	dgst := seedRemote(t, backend, data)

	// First call populates cache.
	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	// Second call should hit cache.
	info, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
}

func TestStore_Info_EmptyDigest(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Info(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty digest")
}

func TestStore_Info_NotFound(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Info(context.Background(), digest.FromBytes([]byte("missing")))
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// ReaderAt
// ---------------------------------------------------------------------------

func TestStore_ReaderAt_LocalHit(t *testing.T) {
	s, _, local := testStore(t)
	data := []byte("reader-local")
	dgst := seedLocal(t, local, data)

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
	s, backend, _ := testStore(t)
	data := []byte("reader-remote")
	dgst := seedRemote(t, backend, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	defer ra.Close()

	buf := make([]byte, len(data))
	n, err := ra.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, data, buf[:n])
}

func TestStore_ReaderAt_InvalidDigest(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: "invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid digest")
}

// ---------------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------------

func TestStore_WriterLifecycle(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	data := []byte("writer-lifecycle")
	dgst := digest.FromBytes(data)

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	n, err := w.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)

	err = w.Commit(ctx, int64(len(data)), dgst)
	require.NoError(t, err)
}

func TestStore_Writer_DuplicateIngestion(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := digest.FromBytes([]byte("dup"))

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	// Second writer with same ref should fail.
	_, err = s.Writer(ctx, content.WithRef(dgst.String()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already active")
}

func TestStore_Writer_NoRefOrDigest(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Writer(context.Background())
	require.Error(t, err)
}

func TestStore_Writer_DigestFromRef(t *testing.T) {
	s, _, _ := testStore(t)
	dgst := digest.FromBytes([]byte("ref-digest"))
	ref := "test@" + dgst.String()

	w, err := s.Writer(context.Background(), content.WithRef(ref))
	require.NoError(t, err)
	require.NotNil(t, w)
	w.Close()
}

// ---------------------------------------------------------------------------
// Abort
// ---------------------------------------------------------------------------

func TestStore_Abort(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := digest.FromBytes([]byte("abort-test"))

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	err = s.Abort(ctx, dgst.String())
	require.NoError(t, err)

	// After abort, we should be able to create a new writer with the same ref
	// (proving the ingestion was removed).
	w2, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)
	require.NotNil(t, w2)
	w2.Close()
}

// ---------------------------------------------------------------------------
// Status / ListStatuses
// ---------------------------------------------------------------------------

func TestStore_Status_ActiveIngestion(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := digest.FromBytes([]byte("status-test"))

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	st, err := s.Status(ctx, dgst.String())
	require.NoError(t, err)
	assert.Equal(t, dgst.String(), st.Ref)
}

func TestStore_ListStatuses_Empty(t *testing.T) {
	s, _, _ := testStore(t)
	statuses, err := s.ListStatuses(context.Background())
	require.NoError(t, err)
	assert.Empty(t, statuses)
}

func TestStore_ListStatuses_WithActive(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := digest.FromBytes([]byte("list-status"))

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	statuses, err := s.ListStatuses(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, statuses)
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestStore_Delete(t *testing.T) {
	s, _, local := testStore(t)
	data := []byte("delete-me")
	dgst := seedLocal(t, local, data)

	err := s.Delete(context.Background(), dgst)
	require.NoError(t, err)

	_, err = s.Info(context.Background(), dgst)
	require.Error(t, err)
}

func TestStore_Delete_NotFound(t *testing.T) {
	s, _, _ := testStore(t)
	err := s.Delete(context.Background(), digest.FromBytes([]byte("missing")))
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestStore_Update(t *testing.T) {
	s, _, local := testStore(t)
	data := []byte("update-me")
	dgst := seedLocal(t, local, data)

	updated, err := s.Update(context.Background(), content.Info{
		Digest: dgst,
		Labels: map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, "test", updated.Labels["env"])
}

// ---------------------------------------------------------------------------
// Walk
// ---------------------------------------------------------------------------

func TestStore_Walk(t *testing.T) {
	s, _, local := testStore(t)
	seedLocal(t, local, []byte("walk-a"))
	seedLocal(t, local, []byte("walk-b"))

	var walked []digest.Digest
	err := s.Walk(context.Background(), func(info content.Info) error {
		walked = append(walked, info.Digest)
		return nil
	})
	require.NoError(t, err)
	assert.Len(t, walked, 2)
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestStore_Close(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	dgst := digest.FromBytes([]byte("close-test"))

	_, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	err = s.Close()
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// digestFromRef
// ---------------------------------------------------------------------------

func TestDigestFromRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{"valid", "test@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", false},
		{"empty", "", true},
		{"no at", "test", true},
		{"invalid digest", "test@notadigest", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := digestFromRef(tt.ref)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
