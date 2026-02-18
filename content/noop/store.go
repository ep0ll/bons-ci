package noop

import (
	"context"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type NoopContentStore interface {
	content.Store
}

type noopStore struct{}

// Abort implements NoopContentStore.
func (n *noopStore) Abort(ctx context.Context, ref string) error {
	return nil
}

// Delete implements NoopContentStore.
func (n *noopStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return nil
}

// Info implements NoopContentStore.
func (n *noopStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	return content.Info{}, nil
}

// ListStatuses implements NoopContentStore.
func (n *noopStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return nil, nil
}

// ReaderAt implements NoopContentStore.
func (n *noopStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	return NoopReaderAt(), nil
}

// Status implements NoopContentStore.
func (n *noopStore) Status(ctx context.Context, ref string) (content.Status, error) {
	return content.Status{}, nil
}

// Update implements NoopContentStore.
func (n *noopStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return content.Info{}, nil
}

// Walk implements NoopContentStore.
func (n *noopStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return nil
}

// Writer implements NoopContentStore.
func (n *noopStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return nil, nil
}

var _ NoopContentStore = &noopStore{}

func NoopStore() NoopContentStore {
	return &noopStore{}
}
