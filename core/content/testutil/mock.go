// Package testutil provides configurable mock implementations of content
// store interfaces for use in unit tests.
//
// Each mock field is optional — a nil field falls back to a sensible no-op
// default, so tests only need to set the fields relevant to the behaviour
// being exercised.
package testutil

import (
	"context"

	"github.com/bons/bons-ci/core/content/store/noop"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// MockStore is a configurable content.Store for use in tests.
// Set any Fn field to inject specific behaviour; all others default to no-op.
type MockStore struct {
	AbortFn        func(ctx context.Context, ref string) error
	DeleteFn       func(ctx context.Context, dgst digest.Digest) error
	InfoFn         func(ctx context.Context, dgst digest.Digest) (content.Info, error)
	ListStatusesFn func(ctx context.Context, filters ...string) ([]content.Status, error)
	ReaderAtFn     func(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error)
	StatusFn       func(ctx context.Context, ref string) (content.Status, error)
	UpdateFn       func(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error)
	WalkFn         func(ctx context.Context, fn content.WalkFunc, filters ...string) error
	WriterFn       func(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error)
}

func (m *MockStore) Abort(ctx context.Context, ref string) error {
	if m.AbortFn != nil {
		return m.AbortFn(ctx, ref)
	}
	return nil
}

func (m *MockStore) Delete(ctx context.Context, dgst digest.Digest) error {
	if m.DeleteFn != nil {
		return m.DeleteFn(ctx, dgst)
	}
	return nil
}

func (m *MockStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if m.InfoFn != nil {
		return m.InfoFn(ctx, dgst)
	}
	return content.Info{}, nil
}

func (m *MockStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	if m.ListStatusesFn != nil {
		return m.ListStatusesFn(ctx, filters...)
	}
	return nil, nil
}

func (m *MockStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	if m.ReaderAtFn != nil {
		return m.ReaderAtFn(ctx, desc)
	}
	return noop.ReaderAt(), nil
}

func (m *MockStore) Status(ctx context.Context, ref string) (content.Status, error) {
	if m.StatusFn != nil {
		return m.StatusFn(ctx, ref)
	}
	return content.Status{}, nil
}

func (m *MockStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	if m.UpdateFn != nil {
		return m.UpdateFn(ctx, info, fieldpaths...)
	}
	return info, nil
}

func (m *MockStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	if m.WalkFn != nil {
		return m.WalkFn(ctx, fn, filters...)
	}
	return nil
}

func (m *MockStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	if m.WriterFn != nil {
		return m.WriterFn(ctx, opts...)
	}
	return noop.Writer(), nil
}

var _ content.Store = (*MockStore)(nil)

// MockWriter is a configurable content.Writer for use in tests.
type MockWriter struct {
	WriteFn    func(p []byte) (int, error)
	CommitFn   func(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error
	DigestFn   func() digest.Digest
	StatusFn   func() (content.Status, error)
	TruncateFn func(size int64) error
	CloseFn    func() error
}

func (m *MockWriter) Write(p []byte) (int, error) {
	if m.WriteFn != nil {
		return m.WriteFn(p)
	}
	return len(p), nil
}

func (m *MockWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if m.CommitFn != nil {
		return m.CommitFn(ctx, size, expected, opts...)
	}
	return nil
}

func (m *MockWriter) Digest() digest.Digest {
	if m.DigestFn != nil {
		return m.DigestFn()
	}
	return digest.Digest("")
}

func (m *MockWriter) Status() (content.Status, error) {
	if m.StatusFn != nil {
		return m.StatusFn()
	}
	return content.Status{}, nil
}

func (m *MockWriter) Truncate(size int64) error {
	if m.TruncateFn != nil {
		return m.TruncateFn(size)
	}
	return nil
}

func (m *MockWriter) Close() error {
	if m.CloseFn != nil {
		return m.CloseFn()
	}
	return nil
}

var _ content.Writer = (*MockWriter)(nil)
