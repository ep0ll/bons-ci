package noop

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

// Writer returns a content.Writer that accepts all data and commits without
// error, discarding everything it receives.
func Writer() content.Writer { return &writer{} }

type writer struct{}

// Write discards p and reports it as fully written.
func (w *writer) Write(p []byte) (int, error) { return len(p), nil }

// Commit succeeds unconditionally.
func (w *writer) Commit(_ context.Context, _ int64, _ digest.Digest, _ ...content.Opt) error {
	return nil
}

// Digest returns the zero digest because no real hashing is performed.
func (w *writer) Digest() digest.Digest { return digest.Digest("") }

// Status returns an empty status with no error.
func (w *writer) Status() (content.Status, error) { return content.Status{}, nil }

// Truncate is a no-op.
func (w *writer) Truncate(_ int64) error { return nil }

// Close is a no-op.
func (w *writer) Close() error { return nil }

var _ content.Writer = (*writer)(nil)
