package b2

import (
	"context"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) (*Store, *mockBackend) {
	t.Helper()
	backend := newMockBackend()
	cfg := Config{
		Bucket:      "test-bucket",
		Region:      "us-east-1",
		Tenant:      "acme",
		BlobsPrefix: "blobs/",
	}
	store, err := New(backend, cfg)
	require.NoError(t, err)
	return store, backend
}

func seedBlob(t *testing.T, store *Store, data []byte) digest.Digest {
	t.Helper()
	ctx := context.Background()
	dgst := digest.FromBytes(data)

	w, err := store.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	_, err = w.Write(data)
	require.NoError(t, err)

	err = w.Commit(ctx, int64(len(data)), dgst)
	require.NoError(t, err)

	return dgst
}

// ---------------------------------------------------------------------------
// Store.Info
// ---------------------------------------------------------------------------

func TestStore_Info(t *testing.T) {
	s, _ := testStore(t)
	data := []byte("hello world")
	dgst := seedBlob(t, s, data)

	info, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
	assert.Equal(t, int64(len(data)), info.Size)
	assert.NotNil(t, info.Labels)
}

func TestStore_Info_NotFound(t *testing.T) {
	s, _ := testStore(t)
	_, err := s.Info(context.Background(), digest.FromBytes([]byte("missing")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NoSuchKey")
}

// ---------------------------------------------------------------------------
// Store.Delete
// ---------------------------------------------------------------------------

func TestStore_Delete(t *testing.T) {
	s, _ := testStore(t)
	dgst := seedBlob(t, s, []byte("to-delete"))

	err := s.Delete(context.Background(), dgst)
	require.NoError(t, err)

	_, err = s.Info(context.Background(), dgst)
	require.Error(t, err)
}

func TestStore_Delete_NotFound(t *testing.T) {
	s, _ := testStore(t)
	err := s.Delete(context.Background(), digest.FromBytes([]byte("missing")))
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Store.Walk
// ---------------------------------------------------------------------------

func TestStore_Walk(t *testing.T) {
	s, _ := testStore(t)
	seedBlob(t, s, []byte("blob-a"))
	seedBlob(t, s, []byte("blob-b"))

	var walked []digest.Digest
	err := s.Walk(context.Background(), func(info content.Info) error {
		walked = append(walked, info.Digest)
		return nil
	})
	require.NoError(t, err)
	assert.Len(t, walked, 2)
}

// ---------------------------------------------------------------------------
// Store.ReaderAt
// ---------------------------------------------------------------------------

func TestStore_ReaderAt(t *testing.T) {
	s, _ := testStore(t)
	data := []byte("reader-at-test")
	dgst := seedBlob(t, s, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{
		Digest: dgst,
		Size:   int64(len(data)),
	})
	require.NoError(t, err)
	defer ra.Close()

	buf := make([]byte, len(data))
	n, err := ra.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, data, buf[:n])
	assert.Equal(t, int64(len(data)), ra.Size())
}

// ---------------------------------------------------------------------------
// Store.Writer — full lifecycle
// ---------------------------------------------------------------------------

func TestStore_WriterLifecycle(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	data := []byte("lifecycle-test-data")
	dgst := digest.FromBytes(data)

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)

	st, err := w.Status()
	require.NoError(t, err)
	assert.Equal(t, int64(0), st.Offset)

	n, err := w.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, dgst, w.Digest())

	err = w.Commit(ctx, int64(len(data)), dgst, content.WithLabels(map[string]string{"env": "test"}))
	require.NoError(t, err)

	// Verify object exists.
	info, err := s.Info(ctx, dgst)
	require.NoError(t, err)
	assert.Equal(t, dgst, info.Digest)
}

// ---------------------------------------------------------------------------
// Store.Writer — no ref/digest error
// ---------------------------------------------------------------------------

func TestStore_Writer_NoRefOrDigest(t *testing.T) {
	s, _ := testStore(t)
	_, err := s.Writer(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a descriptor digest or ref")
}

// ---------------------------------------------------------------------------
// Store.Update
// ---------------------------------------------------------------------------

func TestStore_Update(t *testing.T) {
	s, _ := testStore(t)
	dgst := seedBlob(t, s, []byte("update-me"))

	updated, err := s.Update(context.Background(), content.Info{
		Digest: dgst,
		Labels: map[string]string{"new-label": "value"},
	})
	require.NoError(t, err)
	assert.Equal(t, "value", updated.Labels["new-label"])
}

// ---------------------------------------------------------------------------
// Store.Status — no uploads
// ---------------------------------------------------------------------------

func TestStore_Status_NotFound(t *testing.T) {
	s, _ := testStore(t)
	_, err := s.Status(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no incomplete upload")
}

// ---------------------------------------------------------------------------
// Store.ListStatuses — empty
// ---------------------------------------------------------------------------

func TestStore_ListStatuses_Empty(t *testing.T) {
	s, _ := testStore(t)
	statuses, err := s.ListStatuses(context.Background())
	require.NoError(t, err)
	assert.Empty(t, statuses)
}
