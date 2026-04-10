package registry

import (
	"context"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingHook struct {
	mu     sync.Mutex
	events []Event
}

func (h *recordingHook) OnEvent(ctx context.Context, evt Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, evt)
}

func (h *recordingHook) Events() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Event, len(h.events))
	copy(out, h.events)
	return out
}

func TestStoreHooks_BlobAccessed(t *testing.T) {
	hook := &recordingHook{}
	localStore := NewMockStore()
	store, err := NewStore(localStore, WithReference("docker.io/library/test:latest"), WithHooks(hook))
	require.NoError(t, err)

	dgst := digest.FromBytes([]byte("test"))
	
	// Create blob locally
	localStore.blobs[dgst] = []byte("test")
	localStore.infos[dgst] = content.Info{Digest: dgst, Size: 4}

	_, err = store.Info(context.Background(), dgst)
	require.NoError(t, err)

	events := hook.Events()
	assert.Len(t, events, 1)
	assert.Equal(t, EventBlobAccessed, events[0].Kind)
	assert.Equal(t, dgst, events[0].Digest)
}

func TestStoreHooks_BlobDeleted(t *testing.T) {
	hook := &recordingHook{}
	localStore := NewMockStore()
	store, err := NewStore(localStore, WithReference("docker.io/library/test:latest"), WithHooks(hook))
	require.NoError(t, err)

	dgst := digest.FromBytes([]byte("test"))
	
	localStore.blobs[dgst] = []byte("test")
	localStore.infos[dgst] = content.Info{Digest: dgst, Size: 4}

	err = store.Delete(context.Background(), dgst)
	require.NoError(t, err)

	events := hook.Events()
	assert.Len(t, events, 1)
	assert.Equal(t, EventBlobDeleted, events[0].Kind)
	assert.Equal(t, dgst, events[0].Digest)
}

func TestStoreHooks_BlobCached(t *testing.T) {
	hook := &recordingHook{}
	localStore := NewMockStore()
	store, err := NewStore(localStore, WithReference("docker.io/library/test:latest"), WithHooks(hook))
	require.NoError(t, err)

	dgst := digest.FromBytes([]byte("test"))
	
	// Seed directly to simulate a cache hit in ReaderAt
	localStore.blobs[dgst] = []byte("test")

	desc := v1.Descriptor{Digest: dgst, Size: 4}
	ra, err := store.ReaderAt(context.Background(), desc)
	require.NoError(t, err)
	ra.Close()

	events := hook.Events()
	assert.Len(t, events, 1)
	assert.Equal(t, EventBlobCached, events[0].Kind)
	assert.Equal(t, dgst, events[0].Digest)
	assert.Equal(t, int64(4), events[0].Size)
}
