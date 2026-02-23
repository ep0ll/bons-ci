package content

import (
	"context"
	"fmt"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type localFirstContentStore struct {
	local, registry content.Store
}

// Abort implements content.Store.
func (l *localFirstContentStore) Abort(ctx context.Context, ref string) error {
	if err := l.local.Abort(ctx, ref); err != nil {
		return err
	}

	// Best-effort abort on registry — ignore errors (registry may not have this ref)
	_ = l.registry.Abort(ctx, ref)

	return nil
}

// Delete implements content.Store.
func (l *localFirstContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return l.local.Delete(ctx, dgst)
}

// Info implements content.Store.
func (l *localFirstContentStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if info, err := l.local.Info(ctx, dgst); err == nil {
		return info, nil
	}

	return l.registry.Info(ctx, dgst)
}

// ListStatuses implements content.Store.
func (l *localFirstContentStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return l.local.ListStatuses(ctx, filters...)
}

// ReaderAt implements content.Store.
func (l *localFirstContentStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	readerAt, err := l.local.ReaderAt(ctx, desc)
	if err == nil {
		return readerAt, nil
	}

	return l.registry.ReaderAt(ctx, desc)
}

// Status implements content.Store.
func (l *localFirstContentStore) Status(ctx context.Context, ref string) (content.Status, error) {
	return l.local.Status(ctx, ref)
}

// Update implements content.Store.
func (l *localFirstContentStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return l.local.Update(ctx, info, fieldpaths...)
}

// Walk implements content.Store.
func (l *localFirstContentStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return l.local.Walk(ctx, fn, filters...)
}

// Writer implements content.Store.
// Returns a multiWriter writing to both local and registry when possible.
// At minimum, the local writer must succeed.
func (l *localFirstContentStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	lw, lerr := l.local.Writer(ctx, opts...)
	if lerr != nil {
		return nil, fmt.Errorf("local writer: %w", lerr)
	}

	rw, rerr := l.registry.Writer(ctx, opts...)
	if rerr != nil {
		// Registry writer failed, fall back to local-only
		return lw, nil
	}

	return &multiWriter{writers: []content.Writer{lw, &secondaryWriter{Writer: rw}}}, nil
}

type secondaryWriter struct {
	content.Writer
	mu     sync.Mutex
	failed bool
}

func (s *secondaryWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	failed := s.failed
	s.mu.Unlock()

	if failed {
		return len(p), nil
	}

	n, err := s.Writer.Write(p)
	if err != nil {
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
		s.Writer.Close()
		return len(p), nil
	}
	return n, nil
}

func (s *secondaryWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	s.mu.Lock()
	failed := s.failed
	s.mu.Unlock()

	if failed {
		return nil
	}

	if err := s.Writer.Commit(ctx, size, expected, opts...); err != nil {
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
		s.Writer.Close()
	}
	return nil
}

func (s *secondaryWriter) Truncate(size int64) error {
	s.mu.Lock()
	failed := s.failed
	s.mu.Unlock()

	if failed {
		return nil
	}
	if err := s.Writer.Truncate(size); err != nil {
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
		s.Writer.Close()
	}
	return nil
}

func (s *secondaryWriter) Close() error {
	s.mu.Lock()
	failed := s.failed
	s.mu.Unlock()

	if failed {
		return nil
	}
	return s.Writer.Close()
}

var _ content.Store = &localFirstContentStore{}
