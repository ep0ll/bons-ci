package registry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// ---------------------------------------------------------------------------
// mockBackend — in-memory RegistryBackend for unit tests
// ---------------------------------------------------------------------------

type mockBackend struct {
	mu         sync.Mutex
	blobs      map[digest.Digest][]byte
	descs      map[digest.Digest]v1.Descriptor
	resolveErr error // inject error into Resolve
	fetchErr   error // inject error into Fetch
	pushErr    error // inject error into Push
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		blobs: make(map[digest.Digest][]byte),
		descs: make(map[digest.Digest]v1.Descriptor),
	}
}

func (m *mockBackend) Resolve(_ context.Context, ref string) (string, v1.Descriptor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolveErr != nil {
		return "", v1.Descriptor{}, m.resolveErr
	}
	for _, desc := range m.descs {
		return ref, desc, nil
	}
	return "", v1.Descriptor{}, fmt.Errorf("not found: %s", ref)
}

func (m *mockBackend) Fetch(_ context.Context, _ string, desc v1.Descriptor) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	data, ok := m.blobs[desc.Digest]
	if !ok {
		return nil, fmt.Errorf("not found: %s", desc.Digest)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockBackend) Push(_ context.Context, _ string, desc v1.Descriptor) (content.Writer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pushErr != nil {
		return nil, m.pushErr
	}
	return &mockWriter{backend: m, desc: desc, buf: &bytes.Buffer{}}, nil
}

// seed adds a blob directly to the mock backend, returning its digest.
func (m *mockBackend) seed(data []byte) digest.Digest {
	dgst := digest.FromBytes(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[dgst] = data
	m.descs[dgst] = v1.Descriptor{
		Digest:    dgst,
		Size:      int64(len(data)),
		MediaType: "application/octet-stream",
	}
	return dgst
}

var _ RegistryBackend = (*mockBackend)(nil)

// ---------------------------------------------------------------------------
// mockWriter — content.Writer backed by mockBackend
// ---------------------------------------------------------------------------

type mockWriter struct {
	backend   *mockBackend
	desc      v1.Descriptor
	buf       *bytes.Buffer
	committed bool
	closed    bool
	startedAt time.Time
}

func (w *mockWriter) Write(p []byte) (int, error) {
	if w.committed || w.closed {
		return 0, fmt.Errorf("writer already finalised")
	}
	return w.buf.Write(p)
}

func (w *mockWriter) Commit(_ context.Context, _ int64, expected digest.Digest, _ ...content.Opt) error {
	if w.committed {
		return fmt.Errorf("already committed")
	}
	w.committed = true
	data := w.buf.Bytes()
	dgst := expected
	if dgst == "" {
		dgst = digest.FromBytes(data)
	}
	w.backend.mu.Lock()
	defer w.backend.mu.Unlock()
	w.backend.blobs[dgst] = append([]byte(nil), data...)
	w.backend.descs[dgst] = v1.Descriptor{
		Digest:    dgst,
		Size:      int64(len(data)),
		MediaType: "application/octet-stream",
	}
	return nil
}

func (w *mockWriter) Close() error           { w.closed = true; return nil }
func (w *mockWriter) Digest() digest.Digest  { return digest.FromBytes(w.buf.Bytes()) }
func (w *mockWriter) Truncate(_ int64) error { w.buf.Reset(); return nil }
func (w *mockWriter) Status() (content.Status, error) {
	return content.Status{Offset: int64(w.buf.Len()), Total: w.desc.Size}, nil
}

var _ content.Writer = (*mockWriter)(nil)

// ---------------------------------------------------------------------------
// mockLocalStore — in-memory content.Store for local cache testing
// ---------------------------------------------------------------------------

type mockLocalStore struct {
	mu      sync.Mutex
	blobs   map[digest.Digest][]byte
	labels  map[digest.Digest]map[string]string
	writers map[string]*mockLocalWriter
}

func newMockLocalStore() *mockLocalStore {
	return &mockLocalStore{
		blobs:   make(map[digest.Digest][]byte),
		labels:  make(map[digest.Digest]map[string]string),
		writers: make(map[string]*mockLocalWriter),
	}
}

func (s *mockLocalStore) Info(_ context.Context, dgst digest.Digest) (content.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.blobs[dgst]
	if !ok {
		return content.Info{}, fmt.Errorf("not found: %s", dgst)
	}
	return content.Info{Digest: dgst, Size: int64(len(data)), Labels: s.labels[dgst]}, nil
}

func (s *mockLocalStore) ReaderAt(_ context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.blobs[desc.Digest]
	if !ok {
		return nil, fmt.Errorf("not found: %s", desc.Digest)
	}
	return &mockReaderAt{Reader: bytes.NewReader(data), size: int64(len(data))}, nil
}

func (s *mockLocalStore) Writer(_ context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wOpts content.WriterOpts
	for _, opt := range opts {
		opt(&wOpts)
	}
	ref := wOpts.Ref
	if ref == "" {
		ref = wOpts.Desc.Digest.String()
	}
	w := &mockLocalWriter{store: s, ref: ref, desc: wOpts.Desc, buf: &bytes.Buffer{}}
	s.mu.Lock()
	s.writers[ref] = w
	s.mu.Unlock()
	return w, nil
}

func (s *mockLocalStore) Update(_ context.Context, info content.Info, _ ...string) (content.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blobs[info.Digest]; !ok {
		return content.Info{}, fmt.Errorf("not found: %s", info.Digest)
	}
	if s.labels[info.Digest] == nil {
		s.labels[info.Digest] = make(map[string]string)
	}
	for k, v := range info.Labels {
		s.labels[info.Digest][k] = v
	}
	return content.Info{
		Digest: info.Digest,
		Size:   int64(len(s.blobs[info.Digest])),
		Labels: s.labels[info.Digest],
	}, nil
}

func (s *mockLocalStore) Walk(_ context.Context, fn content.WalkFunc, _ ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for dgst, data := range s.blobs {
		if err := fn(content.Info{Digest: dgst, Size: int64(len(data)), Labels: s.labels[dgst]}); err != nil {
			return err
		}
	}
	return nil
}

func (s *mockLocalStore) Delete(_ context.Context, dgst digest.Digest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blobs[dgst]; !ok {
		return fmt.Errorf("not found: %s", dgst)
	}
	delete(s.blobs, dgst)
	delete(s.labels, dgst)
	return nil
}

func (s *mockLocalStore) Status(_ context.Context, ref string) (content.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.writers[ref]
	if !ok {
		return content.Status{}, fmt.Errorf("no active ingestion: %s", ref)
	}
	return content.Status{Ref: ref, Offset: int64(w.buf.Len())}, nil
}

func (s *mockLocalStore) ListStatuses(context.Context, ...string) ([]content.Status, error) {
	return nil, nil
}

func (s *mockLocalStore) Abort(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.writers[ref]; !ok {
		return fmt.Errorf("no active ingestion: %s", ref)
	}
	delete(s.writers, ref)
	return nil
}

// commit seeds a blob directly into the local store (used by test helpers).
func (s *mockLocalStore) commit(dgst digest.Digest, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[dgst] = append([]byte(nil), data...)
}

var _ content.Store = (*mockLocalStore)(nil)

// ---------------------------------------------------------------------------
// mockLocalWriter
// ---------------------------------------------------------------------------

type mockLocalWriter struct {
	store *mockLocalStore
	ref   string
	desc  v1.Descriptor
	buf   *bytes.Buffer
}

func (w *mockLocalWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *mockLocalWriter) Close() error                { return nil }
func (w *mockLocalWriter) Digest() digest.Digest       { return digest.FromBytes(w.buf.Bytes()) }
func (w *mockLocalWriter) Truncate(_ int64) error      { w.buf.Reset(); return nil }
func (w *mockLocalWriter) Status() (content.Status, error) {
	return content.Status{Ref: w.ref, Offset: int64(w.buf.Len())}, nil
}
func (w *mockLocalWriter) Commit(_ context.Context, _ int64, expected digest.Digest, _ ...content.Opt) error {
	data := w.buf.Bytes()
	if expected == "" {
		expected = digest.FromBytes(data)
	}
	w.store.commit(expected, data)
	return nil
}

var _ content.Writer = (*mockLocalWriter)(nil)

// ---------------------------------------------------------------------------
// mockReaderAt — implements content.ReaderAt over a bytes.Reader
// ---------------------------------------------------------------------------

type mockReaderAt struct {
	*bytes.Reader
	size int64
}

func (r *mockReaderAt) Close() error { return nil }
func (r *mockReaderAt) Size() int64  { return r.size }

var _ content.ReaderAt = (*mockReaderAt)(nil)

// ---------------------------------------------------------------------------
// Shared test helpers (used from multiple _test.go files in same package)
// ---------------------------------------------------------------------------

// makeDigest creates a deterministic digest from a string — used in benchmarks.
func makeDigest(s string) digest.Digest { return digest.FromBytes([]byte(s)) }

// seedLocal commits data directly to the local store and returns its digest.
// Works with *testing.T, *testing.B, or any testing.TB.
func seedLocal(t testing.TB, l *mockLocalStore, data []byte) digest.Digest {
	t.Helper()
	dgst := digest.FromBytes(data)
	l.commit(dgst, data)
	return dgst
}

// seedRemote adds data to the mock backend and returns its digest.
func seedRemote(t testing.TB, b *mockBackend, data []byte) digest.Digest {
	t.Helper()
	return b.seed(data)
}
