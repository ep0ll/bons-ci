package b2

import (
	"context"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// contentWriter — Write + Digest
// ---------------------------------------------------------------------------

func TestWriter_WriteAndDigest(t *testing.T) {
	backend := newMockBackend()
	data := []byte("writer-test-data")
	expected := digest.FromBytes(data)

	w, err := newContentWriter(context.Background(), backend, "b", "key/test", "ref", -1, nil)
	require.NoError(t, err)

	n, err := w.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, expected, w.Digest())
}

// ---------------------------------------------------------------------------
// contentWriter — Commit success
// ---------------------------------------------------------------------------

func TestWriter_CommitSuccess(t *testing.T) {
	backend := newMockBackend()
	data := []byte("commit-success")
	dgst := digest.FromBytes(data)

	w, err := newContentWriter(context.Background(), backend, "b", "key/commit", "ref", -1, nil)
	require.NoError(t, err)

	_, err = w.Write(data)
	require.NoError(t, err)

	err = w.Commit(context.Background(), int64(len(data)), dgst)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// contentWriter — double commit
// ---------------------------------------------------------------------------

func TestWriter_DoubleCommit(t *testing.T) {
	backend := newMockBackend()
	data := []byte("double")
	dgst := digest.FromBytes(data)

	w, err := newContentWriter(context.Background(), backend, "b", "key/dc", "ref", -1, nil)
	require.NoError(t, err)

	_, err = w.Write(data)
	require.NoError(t, err)

	err = w.Commit(context.Background(), int64(len(data)), dgst)
	require.NoError(t, err)

	err = w.Commit(context.Background(), int64(len(data)), dgst)
	require.Error(t, err)
	assert.ErrorIs(t, err, errdefs.ErrFailedPrecondition)
}

// ---------------------------------------------------------------------------
// contentWriter — size mismatch
// ---------------------------------------------------------------------------

func TestWriter_SizeMismatch(t *testing.T) {
	backend := newMockBackend()
	data := []byte("size-check")

	w, err := newContentWriter(context.Background(), backend, "b", "key/sz", "ref", -1, nil)
	require.NoError(t, err)

	_, err = w.Write(data)
	require.NoError(t, err)

	err = w.Commit(context.Background(), 999, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size mismatch")
}

// ---------------------------------------------------------------------------
// contentWriter — digest mismatch
// ---------------------------------------------------------------------------

func TestWriter_DigestMismatch(t *testing.T) {
	backend := newMockBackend()
	data := []byte("digest-check")

	w, err := newContentWriter(context.Background(), backend, "b", "key/dg", "ref", -1, nil)
	require.NoError(t, err)

	_, err = w.Write(data)
	require.NoError(t, err)

	err = w.Commit(context.Background(), 0, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "digest mismatch")
}

// ---------------------------------------------------------------------------
// contentWriter — Status
// ---------------------------------------------------------------------------

func TestWriter_Status(t *testing.T) {
	backend := newMockBackend()
	w, err := newContentWriter(context.Background(), backend, "b", "key/st", "my-ref", 100, nil)
	require.NoError(t, err)

	st, err := w.Status()
	require.NoError(t, err)
	assert.Equal(t, "my-ref", st.Ref)
	assert.Equal(t, int64(100), st.Total)
	assert.Equal(t, int64(0), st.Offset)
}

// ---------------------------------------------------------------------------
// contentWriter — Truncate not supported
// ---------------------------------------------------------------------------

func TestWriter_Truncate(t *testing.T) {
	w := &contentWriter{}
	err := w.Truncate(42)
	require.Error(t, err)
	assert.ErrorIs(t, err, errdefs.ErrNotImplemented)
}

// ---------------------------------------------------------------------------
// contentWriter — empty path rejected
// ---------------------------------------------------------------------------

func TestWriter_EmptyPath(t *testing.T) {
	backend := newMockBackend()
	_, err := newContentWriter(context.Background(), backend, "b", "", "ref", -1, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

// ---------------------------------------------------------------------------
// contentWriter — Close waits for upload
// ---------------------------------------------------------------------------

func TestWriter_Close(t *testing.T) {
	backend := newMockBackend()
	w, err := newContentWriter(context.Background(), backend, "b", "key/close", "ref", -1, nil)
	require.NoError(t, err)

	_, err = w.Write([]byte("close-test"))
	require.NoError(t, err)

	err = w.Close()
	require.NoError(t, err)
}
