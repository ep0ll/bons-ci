package registry

import (
	"context"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ---------------------------------------------------------------------------
// Recording tracer infrastructure
// ---------------------------------------------------------------------------

type recordedSpan struct {
	name string
	err  error
}

type recordingTracer struct {
	noop.Tracer
	mu    sync.Mutex
	spans []recordedSpan
}

func (r *recordingTracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	r.mu.Lock()
	r.spans = append(r.spans, recordedSpan{name: name})
	r.mu.Unlock()
	return noop.NewTracerProvider().Tracer("").Start(ctx, name)
}

func (r *recordingTracer) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, len(r.spans))
	for i, s := range r.spans {
		names[i] = s.name
	}
	return names
}

func (r *recordingTracer) first() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.spans) == 0 {
		return ""
	}
	return r.spans[0].name
}

type recordingTP struct {
	noop.TracerProvider
	tracer *recordingTracer
}

func newRecordingTP() *recordingTP { return &recordingTP{tracer: &recordingTracer{}} }

func (p *recordingTP) Tracer(_ string, _ ...trace.TracerOption) trace.Tracer { return p.tracer }

// tracedStore builds a TracedStore backed by mocks and a recording tracer.
func tracedStore(t *testing.T) (*TracedStore, *mockBackend, *mockLocalStore, *recordingTracer) {
	t.Helper()
	b := newMockBackend()
	l := newMockLocalStore()
	s, err := New(b, l, "docker.io/library/test", WithRetryMax(1))
	require.NoError(t, err)
	tp := newRecordingTP()
	return NewTracedStore(s, tp), b, l, tp.tracer
}

// assertFirstSpan asserts the first recorded span has the given name.
func assertFirstSpan(t *testing.T, rec *recordingTracer, want string) {
	t.Helper()
	require.NotEmpty(t, rec.names(), "no spans recorded")
	assert.Equal(t, want, rec.first())
}

// ---------------------------------------------------------------------------
// Per-method span tests
// ---------------------------------------------------------------------------

func TestTracedStore_Info_SpanCreated(t *testing.T) {
	ts, _, l, rec := tracedStore(t)
	dgst := seedLocal(t, l, []byte("traced-info"))
	_, err := ts.Info(context.Background(), dgst)
	require.NoError(t, err)
	assertFirstSpan(t, rec, "registry.Info")
}

func TestTracedStore_Info_ErrorSpanRecorded(t *testing.T) {
	ts, _, _, rec := tracedStore(t)
	_, err := ts.Info(context.Background(), makeDigest("missing"))
	require.Error(t, err)
	assertFirstSpan(t, rec, "registry.Info")
}

func TestTracedStore_ReaderAt_SpanCreated(t *testing.T) {
	ts, _, l, rec := tracedStore(t)
	data := []byte("traced-reader")
	dgst := seedLocal(t, l, data)
	ra, err := ts.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	ra.Close()
	assertFirstSpan(t, rec, "registry.ReaderAt")
}

func TestTracedStore_ReaderAt_ErrorSpanRecorded(t *testing.T) {
	ts, _, _, rec := tracedStore(t)
	_, err := ts.ReaderAt(context.Background(), v1.Descriptor{Digest: "bad"})
	require.Error(t, err)
	assertFirstSpan(t, rec, "registry.ReaderAt")
}

func TestTracedStore_Writer_SpanCreated(t *testing.T) {
	ts, _, _, rec := tracedStore(t)
	dgst := makeDigest("traced-writer")
	w, err := ts.Writer(context.Background(), content.WithRef(dgst.String()))
	require.NoError(t, err)
	w.Close()
	assertFirstSpan(t, rec, "registry.Writer")
}

func TestTracedStore_Delete_SpanCreated(t *testing.T) {
	ts, _, l, rec := tracedStore(t)
	dgst := seedLocal(t, l, []byte("traced-delete"))
	require.NoError(t, ts.Delete(context.Background(), dgst))
	assertFirstSpan(t, rec, "registry.Delete")
}

func TestTracedStore_Delete_ErrorSpanRecorded(t *testing.T) {
	ts, _, _, rec := tracedStore(t)
	err := ts.Delete(context.Background(), makeDigest("missing"))
	require.Error(t, err)
	assertFirstSpan(t, rec, "registry.Delete")
}

func TestTracedStore_Status_SpanCreated(t *testing.T) {
	ts, _, _, rec := tracedStore(t)
	_, _ = ts.Status(context.Background(), "nonexistent-ref")
	assertFirstSpan(t, rec, "registry.Status")
}

func TestTracedStore_ListStatuses_SpanCreated(t *testing.T) {
	ts, _, _, rec := tracedStore(t)
	_, err := ts.ListStatuses(context.Background())
	require.NoError(t, err)
	assertFirstSpan(t, rec, "registry.ListStatuses")
}

func TestTracedStore_Abort_SpanCreated(t *testing.T) {
	ts, _, _, rec := tracedStore(t)
	_ = ts.Abort(context.Background(), "some-ref")
	assertFirstSpan(t, rec, "registry.Abort")
}

func TestTracedStore_Walk_SpanCreated(t *testing.T) {
	ts, _, l, rec := tracedStore(t)
	seedLocal(t, l, []byte("traced-walk"))
	err := ts.Walk(context.Background(), func(content.Info) error { return nil })
	require.NoError(t, err)
	assertFirstSpan(t, rec, "registry.Walk")
}

func TestTracedStore_Update_SpanCreated(t *testing.T) {
	ts, _, l, rec := tracedStore(t)
	dgst := seedLocal(t, l, []byte("traced-update"))
	_, err := ts.Update(context.Background(), content.Info{
		Digest: dgst,
		Labels: map[string]string{"k": "v"},
	})
	require.NoError(t, err)
	assertFirstSpan(t, rec, "registry.Update")
}

// ---------------------------------------------------------------------------
// Constructor edge cases
// ---------------------------------------------------------------------------

func TestNewTracedStore_NilProviderUsesGlobal(t *testing.T) {
	s, err := New(newMockBackend(), newMockLocalStore(), "docker.io/library/test", WithRetryMax(1))
	require.NoError(t, err)
	ts := NewTracedStore(s, nil)
	require.NotNil(t, ts)
}

func TestTracedStore_ImplementsContentStore(t *testing.T) {
	var _ content.Store = (*TracedStore)(nil)
}

// ---------------------------------------------------------------------------
// Walk span includes walked_count attribute (behaviour test)
// ---------------------------------------------------------------------------

func TestTracedStore_Walk_RecordsWalkedCount(t *testing.T) {
	ts, _, l, _ := tracedStore(t)
	for i := 0; i < 5; i++ {
		seedLocal(t, l, []byte{byte(i)})
	}

	count := 0
	err := ts.Walk(context.Background(), func(content.Info) error {
		count++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 5, count)
}

// ---------------------------------------------------------------------------
// Concurrent usage — race detector
// ---------------------------------------------------------------------------

func TestTracedStore_ConcurrentInfo_RaceFree(t *testing.T) {
	ts, b, _, _ := tracedStore(t)
	dgst := seedRemote(t, b, []byte("concurrent-traced"))

	var wg sync.WaitGroup
	const goroutines = 50
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ts.Info(context.Background(), dgst)
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkTracedStore_Info(b *testing.B) {
	backend := newMockBackend()
	local := newMockLocalStore()
	s, err := New(backend, local, "docker.io/library/bench", WithRetryMax(1))
	require.NoError(b, err)
	ts := NewTracedStore(s, newRecordingTP())
	dgst := seedLocalB(b, local, make([]byte, 1<<16))
	// Warm cache.
	ts.Info(context.Background(), dgst)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ts.Info(context.Background(), dgst)
		}
	})
}

