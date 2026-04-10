package registry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// MockStore is a concurrent, in-memory implementation of content.Store for testing.
type MockStore struct {
	mu     sync.RWMutex
	blobs  map[digest.Digest][]byte
	infos  map[digest.Digest]content.Info
	active map[string]*mockWriter // ref -> writer
}

func NewMockStore() *MockStore {
	return &MockStore{
		blobs:  make(map[digest.Digest][]byte),
		infos:  make(map[digest.Digest]content.Info),
		active: make(map[string]*mockWriter),
	}
}

func (s *MockStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.infos[dgst]
	if !ok {
		return content.Info{}, errdefs.ErrNotFound
	}
	return info, nil
}

func (s *MockStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.infos[info.Digest]; !ok {
		return content.Info{}, errdefs.ErrNotFound
	}
	// For testing, just replace the entire info
	info.UpdatedAt = time.Now()
	s.infos[info.Digest] = info
	return info, nil
}

func (s *MockStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, info := range s.infos {
		if err := fn(info); err != nil {
			return err
		}
	}
	return nil
}

func (s *MockStore) Delete(ctx context.Context, dgst digest.Digest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.infos[dgst]
	if !ok {
		return errdefs.ErrNotFound
	}
	delete(s.infos, dgst)
	delete(s.blobs, dgst)
	return nil
}

func (s *MockStore) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.blobs[desc.Digest]
	if !ok {
		return nil, errdefs.ErrNotFound
	}
	return mockReaderAt{bytes.NewReader(b)}, nil
}

type mockReaderAt struct {
	*bytes.Reader
}

func (m mockReaderAt) Close() error { return nil }

func (s *MockStore) Status(ctx context.Context, ref string) (content.Status, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.active[ref]
	if !ok {
		return content.Status{}, errdefs.ErrNotFound
	}
	st, _ := w.Status()
	return st, nil
}

func (s *MockStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var statuses []content.Status
	for _, w := range s.active {
		st, _ := w.Status()
		statuses = append(statuses, st)
	}
	return statuses, nil
}

func (s *MockStore) Abort(ctx context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.active[ref]; !ok {
		return errdefs.ErrNotFound
	}
	delete(s.active, ref)
	return nil
}

func (s *MockStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var opt content.WriterOpts
	for _, o := range opts {
		_ = o(&opt)
	}
	ref := opt.Ref
	if ref == "" {
		ref = fmt.Sprintf("random-ref-%d", time.Now().UnixNano())
	}
	
	w := &mockWriter{
		s:         s,
		ref:       ref,
		desc:      opt.Desc,
		buf:       bytes.NewBuffer(nil),
		startedAt: time.Now(),
		updatedAt: time.Now(),
	}
	s.mu.Lock()
	s.active[ref] = w
	s.mu.Unlock()
	return w, nil
}

type mockWriter struct {
	s         *MockStore
	ref       string
	desc      ocispec.Descriptor
	buf       *bytes.Buffer
	startedAt time.Time
	updatedAt time.Time
	closed    bool
}

func (w *mockWriter) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, fmt.Errorf("writer closed")
	}
	w.updatedAt = time.Now()
	return w.buf.Write(p)
}

func (w *mockWriter) Close() error {
	w.closed = true
	return nil
}

func (w *mockWriter) Digest() digest.Digest {
	return digest.FromBytes(w.buf.Bytes())
}

func (w *mockWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if w.closed {
		return fmt.Errorf("writer closed")
	}
	d := w.Digest()
	if expected != "" && expected != d {
		return fmt.Errorf("unexpected digest")
	}
	if size > 0 && size != int64(w.buf.Len()) {
		return fmt.Errorf("unexpected size")
	}

	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	
	w.s.blobs[d] = w.buf.Bytes()
	
	info := content.Info{
		Digest:    d,
		Size:      int64(w.buf.Len()),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if w.desc.MediaType != "" {
		info.Labels = map[string]string{
			"test-mediatype": w.desc.MediaType,
		}
	}
	
	w.s.infos[d] = info
	delete(w.s.active, w.ref)
	w.closed = true
	return nil
}

func (w *mockWriter) Status() (content.Status, error) {
	return content.Status{
		Ref:       w.ref,
		Offset:    int64(w.buf.Len()),
		Total:     w.desc.Size,
		Expected:  w.desc.Digest,
		StartedAt: w.startedAt,
		UpdatedAt: w.updatedAt,
	}, nil
}

func (w *mockWriter) Truncate(size int64) error {
	if size != 0 {
		return fmt.Errorf("truncate to non-zero not supported")
	}
	w.buf.Reset()
	return nil
}

// MockRegistryRepo mocks RegistryRepo for testing.
type MockRegistryRepo struct {
	GetFunc    func(ctx context.Context, ref string) (*registry.OCIRegistry, error)
	PutFunc    func(ctx context.Context, ref string, opts ...registry.Opt) (*registry.OCIRegistry, error)
	ExistsFunc func(ctx context.Context, ref string) (bool, error)
}

func (m *MockRegistryRepo) Get(ctx context.Context, ref string) (*registry.OCIRegistry, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, ref)
	}
	return nil, ErrRegistryNotFound
}

func (m *MockRegistryRepo) Put(ctx context.Context, ref string, opts ...registry.Opt) (*registry.OCIRegistry, error) {
	if m.PutFunc != nil {
		return m.PutFunc(ctx, ref, opts...)
	}
	return nil, nil
}

func (m *MockRegistryRepo) Exists(ctx context.Context, ref string) (bool, error) {
	if m.ExistsFunc != nil {
		return m.ExistsFunc(ctx, ref)
	}
	return true, nil
}

// MockFetcher is a barebones transfer.Fetcher.
type MockFetcher struct {
	FetchFunc func(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error)
}

func (m *MockFetcher) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	if m.FetchFunc != nil {
		return m.FetchFunc(ctx, desc)
	}
	return nil, errdefs.ErrNotFound
}
