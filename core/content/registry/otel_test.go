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
	localStore := NewMockStore()
	store, err := NewStore(localStore, WithReference("docker.io/library/test:latest"))
	require.NoError(t, err)

	tp, recorder := newRecordingTP()
	traced := NewTracedStore(store, tp)
	return traced, recorder
}

func TestTracedStore_Info_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, _ = store.Info(context.Background(), digest.FromBytes([]byte("test")))
	assert.Contains(t, recorder.names(), "registry.Info")
}

func TestTracedStore_Delete_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_ = store.Delete(context.Background(), digest.FromBytes([]byte("test")))
	assert.Contains(t, recorder.names(), "registry.Delete")
}

func TestTracedStore_ReaderAt_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, _ = store.ReaderAt(context.Background(), v1.Descriptor{Digest: digest.FromBytes([]byte("read")), Size: 4})
	assert.Contains(t, recorder.names(), "registry.ReaderAt")
}

func TestTracedStore_Walk_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_ = store.Walk(context.Background(), func(content.Info) error { return nil })
	assert.Contains(t, recorder.names(), "registry.Walk")
}

func TestTracedStore_Writer_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, _ = store.Writer(context.Background(), content.WithRef("ref1"))
	assert.Contains(t, recorder.names(), "registry.Writer")
}

func TestTracedStore_Abort_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_ = store.Abort(context.Background(), "some-ref")
	assert.Contains(t, recorder.names(), "registry.Abort")
}

func TestTracedStore_Status_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, _ = store.Status(context.Background(), "some-ref")
	assert.Contains(t, recorder.names(), "registry.Status")
}

func TestTracedStore_ListStatuses_CreatesSpan(t *testing.T) {
	store, recorder := tracedTestStore(t)
	_, _ = store.ListStatuses(context.Background())
	assert.Contains(t, recorder.names(), "registry.ListStatuses")
}
