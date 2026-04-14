// Package split provides a content.Store that separates read and write
// responsibilities across different backing stores.
//
// This is useful when reads should be served from a fast local cache while
// writes are fanned out to one or more durable targets.
//
// Read path: the dedicated read store is tried first; if it misses, each write
// store is tried in order as a fallback.
//
// Write path: all write stores receive every write operation. If at least one
// write store produces a writer, the operation proceeds via a fanout writer.
package split

import (
	"context"
	"errors"
	"fmt"

	writerfanout "github.com/bons/bons-ci/core/content/writer/fanout"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// New returns a content.Store that reads from read and fans writes out to each
// of the provided writes stores. At least one write store is required.
func New(read content.Store, writes ...content.Store) (content.Store, error) {
	if len(writes) == 0 {
		return nil, fmt.Errorf("split store requires at least one write store")
	}
	return &store{read: read, writes: writes}, nil
}

// MustNew is like New but panics on configuration errors. Intended for use in
// program initialisation where misconfiguration is a programmer error.
func MustNew(read content.Store, writes ...content.Store) content.Store {
	s, err := New(read, writes...)
	if err != nil {
		panic(err)
	}
	return s
}

type store struct {
	read   content.Store
	writes []content.Store
}

// Info tries write stores first (they are the source of truth for active
// writes), then falls back to the read store.
func (s *store) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	for _, ws := range s.writes {
		if info, err := ws.Info(ctx, dgst); err == nil {
			return info, nil
		}
	}
	return s.read.Info(ctx, dgst)
}

// ReaderAt reads from the dedicated read store first; if it misses, tries
// each write store in order.
func (s *store) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	if r, err := s.read.ReaderAt(ctx, desc); err == nil {
		return r, nil
	}
	for _, ws := range s.writes {
		if r, err := ws.ReaderAt(ctx, desc); err == nil {
			return r, nil
		}
	}
	return nil, fmt.Errorf("split: content not found in any store")
}

// ListStatuses returns in-progress statuses from the first write store that
// succeeds. Write stores are authoritative for in-flight operations.
func (s *store) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	for _, ws := range s.writes {
		if statuses, err := ws.ListStatuses(ctx, filters...); err == nil {
			return statuses, nil
		}
	}
	return nil, fmt.Errorf("split: no write store could list statuses")
}

// Status returns the write status from the first write store that has it.
func (s *store) Status(ctx context.Context, ref string) (content.Status, error) {
	for _, ws := range s.writes {
		if st, err := ws.Status(ctx, ref); err == nil {
			return st, nil
		}
	}
	return content.Status{}, fmt.Errorf("split: status %q not found in any write store", ref)
}

// Writer opens writers across all write stores and returns a fanout writer.
// Returns an error only if no write store could open a writer.
func (s *store) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var (
		writers []content.Writer
		errs    []error
	)
	for _, ws := range s.writes {
		w, err := ws.Writer(ctx, opts...)
		if err != nil {
			errs = append(errs, err)
		} else {
			writers = append(writers, w)
		}
	}
	if len(writers) == 0 {
		return nil, errors.Join(errs...)
	}
	return writerfanout.New(writers...), nil
}

// Abort aborts the in-progress write ref across all write stores.
// Returns the first error encountered; all stores are always attempted.
func (s *store) Abort(ctx context.Context, ref string) (retErr error) {
	for _, ws := range s.writes {
		if err := ws.Abort(ctx, ref); err != nil && retErr == nil {
			retErr = err
		}
	}
	return retErr
}

// Delete removes dgst from the read store and all write stores.
func (s *store) Delete(ctx context.Context, dgst digest.Digest) (retErr error) {
	if err := s.read.Delete(ctx, dgst); err != nil {
		retErr = err
	}
	for _, ws := range s.writes {
		if err := ws.Delete(ctx, dgst); err != nil && retErr == nil {
			retErr = err
		}
	}
	return retErr
}

// Update updates metadata across all write stores. Returns the last successful
// result; any errors from write stores are joined.
func (s *store) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	var (
		lastInfo content.Info
		errs     []error
		updated  bool
	)
	for _, ws := range s.writes {
		upd, err := ws.Update(ctx, info, fieldpaths...)
		if err != nil {
			errs = append(errs, err)
		} else {
			lastInfo = upd
			updated = true
		}
	}
	if updated {
		return lastInfo, errors.Join(errs...)
	}
	return content.Info{}, errors.Join(errs...)
}

// Walk iterates over the read store. The read store is considered the
// canonical view of committed content.
func (s *store) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return s.read.Walk(ctx, fn, filters...)
}

var _ content.Store = (*store)(nil)
