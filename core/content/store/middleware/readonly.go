package middleware

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

// ErrReadOnly is returned by any mutating operation on a read-only store.
var ErrReadOnly = fmt.Errorf("content store is read-only")

// ReadOnly returns a Middleware that wraps a store and rejects all write
// operations (Writer, Delete, Update, Abort) with ErrReadOnly.
// Read operations (Info, ReaderAt, Status, ListStatuses, Walk) pass through.
func ReadOnly() Middleware {
	return func(next content.Store) content.Store {
		return &readOnlyStore{Store: next}
	}
}

type readOnlyStore struct {
	content.Store
}

// Writer always returns ErrReadOnly.
func (r *readOnlyStore) Writer(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
	return nil, fmt.Errorf("Writer: %w", ErrReadOnly)
}

// Delete always returns ErrReadOnly.
func (r *readOnlyStore) Delete(_ context.Context, _ digest.Digest) error {
	return fmt.Errorf("Delete: %w", ErrReadOnly)
}

// Update always returns ErrReadOnly.
func (r *readOnlyStore) Update(_ context.Context, _ content.Info, _ ...string) (content.Info, error) {
	return content.Info{}, fmt.Errorf("Update: %w", ErrReadOnly)
}

// Abort always returns ErrReadOnly.
func (r *readOnlyStore) Abort(_ context.Context, _ string) error {
	return fmt.Errorf("Abort: %w", ErrReadOnly)
}
