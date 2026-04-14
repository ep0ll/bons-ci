package b2

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

// recordingTracer is a test tracer that records span names.
type recordingTracer struct {
	noop.Tracer
	mu    sync.Mutex
	spans []string
}

func (r *recordingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	r.mu.Lock()
	r.spans = append(r.spans, name)
	r.mu.Unlock()
	return r.Tracer.Start(ctx, name, opts...)
}

func (r *recordingTracer) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.spans))
	copy(out, r.spans)
	return out
}

type recordingTP struct {
	noop.TracerProvider
	tracer *recordingTracer
}

func (p *recordingTP) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return p.tracer
}

func newRecordingTP() (*recordingTP, *recordingTracer) {
	r := &recordingTracer{}
	return &recordingTP{tracer: r}, r
}

func tracedTestStore(t *testing.T) (content.Store, *recordingTracer) {
	t.Helper()
	backend := newMockBackend()
	cfg := Config{Bucket: "b", Tenant: "t", BlobsPrefix: "blobs/"}
	store, err := New(backend, cfg)
	require.NoError(t, err)

	tp, recorder := newRecordingTP()
	traced := NewTracedStore(store, tp)
	return traced, recorder
}

func TestTracedStore_Info_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	inner := store.(*TracedStore).inner.(*Store)
	dgst := seedBlob(t, inner, []byte("traced-info"))

	_, err := store.Info(context.Background(), dgst)
	require.NoError(t, err)

	assert.Contains(t, recorder.names(), "b2.Info")
}

func TestTracedStore_Delete_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	inner := store.(*TracedStore).inner.(*Store)
	dgst := seedBlob(t, inner, []byte("traced-del"))

	err := store.Delete(context.Background(), dgst)
	require.NoError(t, err)

	assert.Contains(t, recorder.names(), "b2.Delete")
}

func TestTracedStore_ReaderAt_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	inner := store.(*TracedStore).inner.(*Store)
	data := []byte("traced-read")
	dgst := seedBlob(t, inner, data)

	ra, err := store.ReaderAt(context.Background(), v1.Descriptor{Digest: dgst, Size: int64(len(data))})
	require.NoError(t, err)
	ra.Close()

	assert.Contains(t, recorder.names(), "b2.ReaderAt")
}

func TestTracedStore_Walk_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	inner := store.(*TracedStore).inner.(*Store)
	seedBlob(t, inner, []byte("walk-a"))

	err := store.Walk(context.Background(), func(content.Info) error { return nil })
	require.NoError(t, err)

	assert.Contains(t, recorder.names(), "b2.Walk")
}

func TestTracedStore_Writer_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)

	dgst := digest.FromBytes([]byte("w"))
	w, err := store.Writer(context.Background(), content.WithRef(dgst.String()))
	require.NoError(t, err)
	w.Close()

	assert.Contains(t, recorder.names(), "b2.Writer")
}

func TestTracedStore_Abort_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_ = store.Abort(context.Background(), "some-ref")

	assert.Contains(t, recorder.names(), "b2.Abort")
}

func TestTracedStore_Status_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, _ = store.Status(context.Background(), "some-ref")

	assert.Contains(t, recorder.names(), "b2.Status")
}

func TestTracedStore_ListStatuses_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, _ = store.ListStatuses(context.Background())

	assert.Contains(t, recorder.names(), "b2.ListStatuses")
}

func TestTracedStore_Info_Error_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, err := store.Info(context.Background(), digest.FromBytes([]byte("missing")))
	require.Error(t, err)

	assert.Contains(t, recorder.names(), "b2.Info")
}
