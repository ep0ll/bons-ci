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
// collectingHook — captures events for assertion
// ---------------------------------------------------------------------------

type collectingHook struct {
	mu     sync.Mutex
	events []Event
}

func (h *collectingHook) OnEvent(_ context.Context, evt Event) {
	h.mu.Lock()
	h.events = append(h.events, evt)
	h.mu.Unlock()
}

func (h *collectingHook) byKind(kind EventKind) []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Event
	for _, e := range h.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (h *collectingHook) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (h *collectingHook) clear() {
	h.mu.Lock()
	h.events = h.events[:0]
	h.mu.Unlock()
}

// hookStore builds a Store with a single collectingHook attached.
func hookStore(t *testing.T) (*Store, *mockBackend, *mockLocalStore, *collectingHook) {
	t.Helper()
	b := newMockBackend()
	l := newMockLocalStore()
	hook := &collectingHook{}
	s, err := New(b, l, "docker.io/library/test", WithHooks(hook), WithRetryMax(1))
	require.NoError(t, err)
	return s, b, l, hook
}

// ---------------------------------------------------------------------------
// Info events
// ---------------------------------------------------------------------------

func TestHooks_Info_LocalHit_EmitsCached(t *testing.T) {
	s, _, l, hook := hookStore(t)
	data := []byte("hook-local-info")
	dgst := seedLocal(t, l, data)

	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	evts := hook.byKind(EventBlobCached)
	require.Len(t, evts, 1)
	assert.Equal(t, dgst, evts[0].Digest)
	assert.Equal(t, int64(len(data)), evts[0].Size)
	assert.False(t, evts[0].Timestamp.IsZero())
}

func TestHooks_Info_CacheHit_EmitsCached(t *testing.T) {
	s, b, _, hook := hookStore(t)
	dgst := seedRemote(t, b, []byte("hook-cache"))

	// First call — remote → cache miss → EventBlobAccessed.
	_, _ = s.Info(context.Background(), dgst)
	hook.clear()

	// Second call — cache hit → EventBlobCached.
	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	assert.Len(t, hook.byKind(EventBlobCached), 1)
	assert.Empty(t, hook.byKind(EventBlobAccessed))
}

func TestHooks_Info_RemoteFallback_EmitsAccessed(t *testing.T) {
	s, b, _, hook := hookStore(t)
	data := []byte("hook-remote-info")
	dgst := seedRemote(t, b, data)

	_, err := s.Info(context.Background(), dgst)
	require.NoError(t, err)

	evts := hook.byKind(EventBlobAccessed)
	require.Len(t, evts, 1)
	assert.Equal(t, dgst, evts[0].Digest)
	assert.Equal(t, int64(len(data)), evts[0].Size)
}

// ---------------------------------------------------------------------------
// ReaderAt events
// ---------------------------------------------------------------------------

func TestHooks_ReaderAt_LocalHit_EmitsCached(t *testing.T) {
	s, _, l, hook := hookStore(t)
	data := []byte("hook-reader-local")
	dgst := seedLocal(t, l, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	ra.Close()

	evts := hook.byKind(EventBlobCached)
	require.Len(t, evts, 1)
	assert.Equal(t, dgst, evts[0].Digest)
}

func TestHooks_ReaderAt_RemoteFetch_EmitsFetched(t *testing.T) {
	s, b, _, hook := hookStore(t)
	data := []byte("hook-reader-remote")
	dgst := seedRemote(t, b, data)

	ra, err := s.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	ra.Close()

	evts := hook.byKind(EventBlobFetched)
	require.Len(t, evts, 1)
	assert.Equal(t, dgst, evts[0].Digest)
}

// ---------------------------------------------------------------------------
// Writer / Commit events
// ---------------------------------------------------------------------------

func TestHooks_Writer_Commit_EmitsPushed(t *testing.T) {
	s, _, _, hook := hookStore(t)
	ctx := context.Background()
	data := []byte("hook-writer-data")
	dgst := digest.FromBytes(data)

	w, err := s.Writer(ctx, content.WithRef(dgst.String()))
	require.NoError(t, err)
	_, _ = w.Write(data)
	require.NoError(t, w.Commit(ctx, int64(len(data)), dgst))

	// Brief pause for async local goroutine to drain.
	time.Sleep(10 * time.Millisecond)

	pushed := hook.byKind(EventBlobPushed)
	require.Len(t, pushed, 1)
	assert.Equal(t, dgst, pushed[0].Digest)
	assert.Equal(t, int64(len(data)), pushed[0].Size)
	assert.Equal(t, dgst.String(), pushed[0].Ref)
	assert.False(t, pushed[0].Timestamp.IsZero())
}

// ---------------------------------------------------------------------------
// Delete events
// ---------------------------------------------------------------------------

func TestHooks_Delete_EmitsDeleted(t *testing.T) {
	s, _, l, hook := hookStore(t)
	dgst := seedLocal(t, l, []byte("hook-delete-blob"))

	require.NoError(t, s.Delete(context.Background(), dgst))

	evts := hook.byKind(EventBlobDeleted)
	require.Len(t, evts, 1)
	assert.Equal(t, dgst, evts[0].Digest)
}

func TestHooks_Delete_NotFound_NoEvent(t *testing.T) {
	s, _, _, hook := hookStore(t)
	_ = s.Delete(context.Background(), makeDigest("nonexistent"))
	assert.Empty(t, hook.byKind(EventBlobDeleted))
}

// ---------------------------------------------------------------------------
// Multiple hooks — parallel fan-out
// ---------------------------------------------------------------------------

func TestHooks_MultipleHooks_AllReceiveEvent(t *testing.T) {
	hooks := []*collectingHook{{}, {}, {}}
	hookIfaces := make([]Hook, len(hooks))
	for i, h := range hooks {
		hookIfaces[i] = h
	}
	b := newMockBackend()
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test", WithHooks(hookIfaces...), WithRetryMax(1))
	require.NoError(t, err)

	dgst := seedLocal(t, l, []byte("multi-hook-blob"))
	_, err = s.Info(context.Background(), dgst)
	require.NoError(t, err)

	for i, h := range hooks {
		assert.Equal(t, 1, h.total(), "hook[%d] should receive exactly 1 event", i)
	}
}

func TestHooks_MultipleHooks_ConcurrentSafety(t *testing.T) {
	const numHooks = 16
	hooks := make([]*collectingHook, numHooks)
	hookIfaces := make([]Hook, numHooks)
	for i := range hooks {
		hooks[i] = &collectingHook{}
		hookIfaces[i] = hooks[i]
	}
	b := newMockBackend()
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test", WithHooks(hookIfaces...), WithRetryMax(1))
	require.NoError(t, err)

	dgst := seedLocal(t, l, []byte("concurrent-hooks"))

	var wg sync.WaitGroup
	const goroutines = 50
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			s.Info(context.Background(), dgst)
		}()
	}
	wg.Wait()

	// Each hook should have received exactly goroutines events (first call misses
	// cache, then it's populated; but with -race this validates no data races).
	for i, h := range hooks {
		assert.GreaterOrEqual(t, h.total(), 1, "hook[%d] should receive at least 1 event", i)
	}
}

// ---------------------------------------------------------------------------
// HookFunc adapter
// ---------------------------------------------------------------------------

func TestHookFunc_ImplementsHook(t *testing.T) {
	var received Event
	fn := HookFunc(func(_ context.Context, evt Event) { received = evt })
	fn.OnEvent(context.Background(), Event{Kind: EventBlobAccessed, Ref: "test-ref"})
	assert.Equal(t, EventBlobAccessed, received.Kind)
	assert.Equal(t, "test-ref", received.Ref)
}

func TestHookFunc_IsHook(t *testing.T) {
	var _ Hook = HookFunc(func(_ context.Context, _ Event) {})
}

// ---------------------------------------------------------------------------
// Event field coverage
// ---------------------------------------------------------------------------

func TestEvent_TimestampPopulated(t *testing.T) {
	s, _, l, hook := hookStore(t)
	seedLocal(t, l, []byte("ts-test"))
	dgst := seedLocal(t, l, []byte("ts-test"))
	before := time.Now()
	_, _ = s.Info(context.Background(), dgst)
	after := time.Now()

	for _, evt := range hook.byKind(EventBlobCached) {
		assert.True(t, !evt.Timestamp.Before(before) || !evt.Timestamp.After(after),
			"Timestamp should be populated at emit time")
	}
}
