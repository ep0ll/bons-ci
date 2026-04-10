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
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// recordingTracer records span names for test assertions.
type recordingTracer struct {
	noop.Tracer
	mu    sync.Mutex
	spans []string
}

func (r *recordingTracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	r.mu.Lock()
	r.spans = append(r.spans, name)
	r.mu.Unlock()
	return noop.NewTracerProvider().Tracer("").Start(ctx, name)
}

func (r *recordingTracer) getSpans() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(r.spans))
	copy(cp, r.spans)
	return cp
}

// recordingTracerProvider returns a recordingTracer.
type recordingTracerProvider struct {
	noop.TracerProvider
	tracer *recordingTracer
}

func newRecordingTracerProvider() *recordingTracerProvider {
	return &recordingTracerProvider{tracer: &recordingTracer{}}
}

func (p *recordingTracerProvider) Tracer(_ string, _ ...trace.TracerOption) trace.Tracer {
	return p.tracer
}

func tracedTestStore(t *testing.T) (*TracedStore, *mockBackend, *mockLocalStore, *recordingTracer) {
	t.Helper()
	backend := newMockBackend()
	local := newMockLocalStore()
	store, err := New(backend, local, "docker.io/library/test")
	require.NoError(t, err)

	tp := newRecordingTracerProvider()
	traced := NewTracedStore(store, tp)
	return traced, backend, local, tp.tracer
}

func TestTracedStore_Info_CreatesSpan(t *testing.T) {
	ts, _, local, recorder := tracedTestStore(t)
	dgst := seedLocal(t, local, []byte("traced-info"))

	_, err := ts.Info(context.Background(), dgst)
	require.NoError(t, err)

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Info", spans[0])
}

func TestTracedStore_Info_Error_RecordsSpan(t *testing.T) {
	ts, _, _, recorder := tracedTestStore(t)

	_, err := ts.Info(context.Background(), digest.FromBytes([]byte("missing")))
	require.Error(t, err)

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Info", spans[0])
}

func TestTracedStore_ReaderAt_CreatesSpan(t *testing.T) {
	ts, _, local, recorder := tracedTestStore(t)
	data := []byte("traced-reader")
	dgst := seedLocal(t, local, data)

	ra, err := ts.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	ra.Close()

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.ReaderAt", spans[0])
}

func TestTracedStore_Writer_CreatesSpan(t *testing.T) {
	ts, _, _, recorder := tracedTestStore(t)
	dgst := digest.FromBytes([]byte("traced-writer"))

	w, err := ts.Writer(context.Background(), content.WithRef(dgst.String()))
	require.NoError(t, err)
	w.Close()

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Writer", spans[0])
}

func TestTracedStore_Delete_CreatesSpan(t *testing.T) {
	ts, _, local, recorder := tracedTestStore(t)
	dgst := seedLocal(t, local, []byte("traced-delete"))

	err := ts.Delete(context.Background(), dgst)
	require.NoError(t, err)

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Delete", spans[0])
}

func TestTracedStore_Status_CreatesSpan(t *testing.T) {
	ts, _, _, recorder := tracedTestStore(t)

	_, _ = ts.Status(context.Background(), "some-ref")

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Status", spans[0])
}

func TestTracedStore_ListStatuses_CreatesSpan(t *testing.T) {
	ts, _, _, recorder := tracedTestStore(t)

	_, err := ts.ListStatuses(context.Background())
	require.NoError(t, err)

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.ListStatuses", spans[0])
}

func TestTracedStore_Abort_CreatesSpan(t *testing.T) {
	ts, _, _, recorder := tracedTestStore(t)

	_ = ts.Abort(context.Background(), "some-ref")

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Abort", spans[0])
}

func TestTracedStore_Walk_CreatesSpan(t *testing.T) {
	ts, _, local, recorder := tracedTestStore(t)
	seedLocal(t, local, []byte("traced-walk"))

	err := ts.Walk(context.Background(), func(info content.Info) error { return nil })
	require.NoError(t, err)

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Walk", spans[0])
}

func TestTracedStore_Update_CreatesSpan(t *testing.T) {
	ts, _, local, recorder := tracedTestStore(t)
	dgst := seedLocal(t, local, []byte("traced-update"))

	_, err := ts.Update(context.Background(), content.Info{
		Digest: dgst,
		Labels: map[string]string{"key": "val"},
	})
	require.NoError(t, err)

	spans := recorder.getSpans()
	require.NotEmpty(t, spans)
	assert.Equal(t, "registry.Update", spans[0])
}

func TestNewTracedStore_NilProvider(t *testing.T) {
	store, err := New(newMockBackend(), newMockLocalStore(), "docker.io/library/test")
	require.NoError(t, err)
	traced := NewTracedStore(store, nil)
	require.NotNil(t, traced)
}
