package noop

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Store returns a content.Store that silently succeeds on every operation
// without persisting any data.
func Store() content.Store { return &store{} }

type store struct{}

func (s *store) Abort(_ context.Context, _ string) error { return nil }

func (s *store) Delete(_ context.Context, _ digest.Digest) error { return nil }

func (s *store) Info(_ context.Context, _ digest.Digest) (content.Info, error) {
	return content.Info{}, nil
}

func (s *store) ListStatuses(_ context.Context, _ ...string) ([]content.Status, error) {
	return nil, nil
}

func (s *store) ReaderAt(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
	return ReaderAt(), nil
}

func (s *store) Status(_ context.Context, _ string) (content.Status, error) {
	return content.Status{}, nil
}

func (s *store) Update(_ context.Context, info content.Info, _ ...string) (content.Info, error) {
	return info, nil
}

func (s *store) Walk(_ context.Context, _ content.WalkFunc, _ ...string) error { return nil }

func (s *store) Writer(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
	return Writer(), nil
}

var _ content.Store = (*store)(nil)
