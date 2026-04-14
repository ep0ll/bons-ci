// Package fallback provides a content.Store that tries a primary store first
// and transparently falls back to a secondary store for reads when the primary
// does not have the requested content.
//
// Write semantics:
//   - The primary writer is required; failure aborts the operation.
//   - The secondary writer receives a best-effort copy via a resilient writer;
//     its failure is silently absorbed and does not affect the caller.
//
// Read semantics:
//   - All reads query the primary first.
//   - On a not-found response from the primary, the secondary is tried.
//   - Any other primary error is returned immediately.
package fallback

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/core/content/writer/fanout"
	"github.com/bons/bons-ci/core/content/writer/resilient"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// New returns a content.Store that reads from primary first and falls back to
// secondary, while writing to both (secondary is best-effort).
func New(primary, secondary content.Store) content.Store {
	return &store{primary: primary, secondary: secondary}
}

type store struct {
	primary   content.Store
	secondary content.Store
}

// Info tries the primary store; on not-found, queries the secondary.
func (s *store) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	info, err := s.primary.Info(ctx, dgst)
	if err == nil {
		return info, nil
	}
	if !errdefs.IsNotFound(err) {
		return content.Info{}, err
	}
	return s.secondary.Info(ctx, dgst)
}

// ReaderAt tries the primary store; on not-found, queries the secondary.
func (s *store) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	r, err := s.primary.ReaderAt(ctx, desc)
	if err == nil {
		return r, nil
	}
	if !errdefs.IsNotFound(err) {
		return nil, err
	}
	return s.secondary.ReaderAt(ctx, desc)
}

// Writer opens a writer on the primary (required) and wraps the secondary in a
// resilient writer so that secondary failures are silently absorbed.
func (s *store) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	pw, err := s.primary.Writer(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("fallback: primary writer: %w", err)
	}

	sw, err := s.secondary.Writer(ctx, opts...)
	if err != nil {
		// Secondary unavailable — proceed with primary only.
		return pw, nil
	}

	return fanout.New(pw, resilient.Wrap(sw)), nil
}

// Abort attempts to abort on both stores. The primary error takes precedence;
// the secondary is always attempted even if the primary fails.
func (s *store) Abort(ctx context.Context, ref string) error {
	primaryErr := s.primary.Abort(ctx, ref)
	_ = s.secondary.Abort(ctx, ref) // best-effort; ignore secondary error
	return primaryErr
}

// Delete removes content from the primary only.
// Secondary content is intentionally left intact (it acts as a cache/backup).
func (s *store) Delete(ctx context.Context, dgst digest.Digest) error {
	return s.primary.Delete(ctx, dgst)
}

// ListStatuses returns in-progress write statuses from the primary only.
func (s *store) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return s.primary.ListStatuses(ctx, filters...)
}

// Status returns the write status from the primary only.
func (s *store) Status(ctx context.Context, ref string) (content.Status, error) {
	return s.primary.Status(ctx, ref)
}

// Update updates metadata on the primary only.
func (s *store) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return s.primary.Update(ctx, info, fieldpaths...)
}

// Walk iterates over content in the primary only.
func (s *store) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return s.primary.Walk(ctx, fn, filters...)
}

var _ content.Store = (*store)(nil)
