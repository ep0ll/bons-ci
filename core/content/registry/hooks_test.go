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

// collectingHook collects all events for assertions.
type collectingHook struct {
	mu     sync.Mutex
	events []Event
}

func (h *collectingHook) OnEvent(_ context.Context, evt Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, evt)
}

func (h *collectingHook) get() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]Event, len(h.events))
	copy(cp, h.events)
	return cp
}

func (h *collectingHook) findByKind(kind EventKind) []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var result []Event
	for _, e := range h.events {
		if e.Kind == kind {
			result = append(result, e)
		}
	}
	return result
}

func testStoreWithHooks(t *testing.T) (*Store, *mockBackend, *mockLocalStore, *collectingHook) {
	t.Helper()
	backend := newMockBackend()
	local := newMockLocalStore()
	hook := &collectingHook{}
	store, err := New(backend, local, "docker.io/library/test", WithHooks(hook))
	require.NoError(t, err)
	return store, backend, local, hook
}

// ---------------------------------------------------------------------------
// Info events
// ---------------------------------------------------------------------------

func TestHooks_Info_LocalHit_EmitsCached(t *testing.T) {
	s, _, local, hook := testStoreWithHooks(t)
	dgst := seedLocal(t, local, []byte("hook-local"))

	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	cached := hook.findByKind(EventBlobCached)
	require.Len(t, cached, 1)
	assert.Equal(t, dgst, cached[0].Digest)
}

func TestHooks_Info_RemoteFallback_EmitsAccessed(t *testing.T) {
	s, backend, _, hook := testStoreWithHooks(t)
	dgst := seedRemote(t, backend, []byte("hook-remote"))

	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	accessed := hook.findByKind(EventBlobAccessed)
	require.Len(t, accessed, 1)
	assert.Equal(t, dgst, accessed[0].Digest)
}

// ---------------------------------------------------------------------------
// ReaderAt events
// ---------------------------------------------------------------------------

func TestHooks_ReaderAt_LocalHit_EmitsCached(t *testing.T) {
	s, _, local, hook := testStoreWithHooks(t)
	data := []byte("hook-reader-local")
	dgst := seedLocal(t, local, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	ra.Close()

	cached := hook.findByKind(EventBlobCached)
	require.Len(t, cached, 1)
}

func TestHooks_ReaderAt_RemoteFetch_EmitsFetched(t *testing.T) {
	s, backend, _, hook := testStoreWithHooks(t)
	data := []byte("hook-reader-remote")
	dgst := seedRemote(t, backend, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	ra.Close()

	fetched := hook.findByKind(EventBlobFetched)
	require.Len(t, fetched, 1)
	assert.Equal(t, dgst, fetched[0].Digest)
}

// ---------------------------------------------------------------------------
// Writer events
// ---------------------------------------------------------------------------

func TestHooks_Writer_Commit_EmitsPushed(t *testing.T) {
	s, _, _, hook := testStoreWithHooks(t)
	data := []byte("hook-writer")
	dgst := digest.FromBytes(data)

	w, err := s.Writer(context.Background(), content.WithRef(dgst.String()))
	require.NoError(t, err)

	_, err = w.Write(data)
	require.NoError(t, err)

	err = w.Commit(context.Background(), int64(len(data)), dgst)
	require.NoError(t, err)

	pushed := hook.findByKind(EventBlobPushed)
	require.Len(t, pushed, 1)
	assert.Equal(t, dgst, pushed[0].Digest)
	assert.Equal(t, int64(len(data)), pushed[0].Size)
}

// ---------------------------------------------------------------------------
// Delete events
// ---------------------------------------------------------------------------

func TestHooks_Delete_EmitsDeleted(t *testing.T) {
	s, _, local, hook := testStoreWithHooks(t)
	dgst := seedLocal(t, local, []byte("hook-delete"))

	err := s.Delete(context.Background(), dgst)
	require.NoError(t, err)

	deleted := hook.findByKind(EventBlobDeleted)
	require.Len(t, deleted, 1)
	assert.Equal(t, dgst, deleted[0].Digest)
}

// ---------------------------------------------------------------------------
// HookFunc adapter
// ---------------------------------------------------------------------------

func TestHookFunc_Adapter(t *testing.T) {
	var received Event
	fn := HookFunc(func(_ context.Context, evt Event) {
		received = evt
	})

	fn.OnEvent(context.Background(), Event{Kind: EventBlobAccessed, Ref: "test"})
	assert.Equal(t, EventBlobAccessed, received.Kind)
	assert.Equal(t, "test", received.Ref)
}
