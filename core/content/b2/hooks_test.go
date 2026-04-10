package b2

import (
	"context"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Hook + HookFunc
// ---------------------------------------------------------------------------

func TestHookFunc_OnEvent(t *testing.T) {
	var received Event
	hook := HookFunc(func(_ context.Context, evt Event) {
		received = evt
	})

	hook.OnEvent(context.Background(), Event{
		Kind:   EventBlobCommitted,
		Digest: digest.FromBytes([]byte("test")),
		Size:   42,
	})

	assert.Equal(t, EventBlobCommitted, received.Kind)
	assert.Equal(t, int64(42), received.Size)
}

// ---------------------------------------------------------------------------
// Store emits hooks on Delete
// ---------------------------------------------------------------------------

func TestStore_DeleteEmitsHook(t *testing.T) {
	backend := newMockBackend()
	cfg := Config{Bucket: "b", Tenant: "t", BlobsPrefix: "blobs/"}

	var events []Event
	hook := HookFunc(func(_ context.Context, evt Event) {
		events = append(events, evt)
	})

	store, err := New(backend, cfg, WithHooks(hook))
	require.NoError(t, err)

	dgst := seedBlob(t, store, []byte("hooked"))

	// Delete emits a single EventBlobDeleted (after the EventBlobCommitted from seed).
	events = nil // reset
	err = store.Delete(context.Background(), dgst)
	require.NoError(t, err)

	require.Len(t, events, 1)
	assert.Equal(t, EventBlobDeleted, events[0].Kind)
	assert.Equal(t, dgst, events[0].Digest)
}

// ---------------------------------------------------------------------------
// Store emits hooks on Commit
// ---------------------------------------------------------------------------

func TestStore_CommitEmitsHook(t *testing.T) {
	backend := newMockBackend()
	cfg := Config{Bucket: "b", Tenant: "t", BlobsPrefix: "blobs/"}

	var events []Event
	hook := HookFunc(func(_ context.Context, evt Event) {
		events = append(events, evt)
	})

	store, err := New(backend, cfg, WithHooks(hook))
	require.NoError(t, err)

	data := []byte("commit-hook-test")
	dgst := digest.FromBytes(data)

	w, err := store.Writer(context.Background(), content.WithRef(dgst.String()))
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	err = w.Commit(context.Background(), int64(len(data)), dgst)
	require.NoError(t, err)

	// Find the committed event.
	var commitEvent *Event
	for i := range events {
		if events[i].Kind == EventBlobCommitted {
			commitEvent = &events[i]
			break
		}
	}

	require.NotNil(t, commitEvent)
	assert.Equal(t, dgst, commitEvent.Digest)
	assert.Equal(t, int64(len(data)), commitEvent.Size)
}

// ---------------------------------------------------------------------------
// Store emits hooks on Info (access)
// ---------------------------------------------------------------------------

func TestStore_InfoEmitsAccess(t *testing.T) {
	backend := newMockBackend()
	cfg := Config{Bucket: "b", Tenant: "t", BlobsPrefix: "blobs/"}

	var events []Event
	hook := HookFunc(func(_ context.Context, evt Event) {
		events = append(events, evt)
	})

	store, err := New(backend, cfg, WithHooks(hook))
	require.NoError(t, err)

	dgst := seedBlob(t, store, []byte("access-me"))

	events = nil
	_, err = store.Info(context.Background(), dgst)
	require.NoError(t, err)

	require.Len(t, events, 1)
	assert.Equal(t, EventBlobAccessed, events[0].Kind)
}

// ---------------------------------------------------------------------------
// Multiple hooks
// ---------------------------------------------------------------------------

func TestStore_MultipleHooks(t *testing.T) {
	backend := newMockBackend()
	cfg := Config{Bucket: "b", Tenant: "t", BlobsPrefix: "blobs/"}

	var count1, count2 int
	h1 := HookFunc(func(context.Context, Event) { count1++ })
	h2 := HookFunc(func(context.Context, Event) { count2++ })

	store, err := New(backend, cfg, WithHooks(h1, h2))
	require.NoError(t, err)

	seedBlob(t, store, []byte("multi"))

	// Both hooks should have been called for the commit event.
	assert.Equal(t, count1, count2)
	assert.Greater(t, count1, 0)
}
