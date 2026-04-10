package registry

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
)

// FanoutStore wraps a primary content store and optionally writes incoming
// content to additional fallback stores concurrently.
// It is used to simultaneously push the incoming registry blobs to both the
// persistent content store and local snapshotters in one pass.
type FanoutStore struct {
	content.Store
	additional []content.Store
}

// NewFanoutStore creates a new FanoutStore with the primary store and
// zero or more additional stores.
func NewFanoutStore(primary content.Store, additional ...content.Store) *FanoutStore {
	return &FanoutStore{
		Store:      primary,
		additional: additional,
	}
}

// Writer returns a multi-writer that tees content into all underlying stores
// simultaneously. If a store already has the content, it is skipped.
func (f *FanoutStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	primaryWriter, err := f.Store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}

	writers := []content.Writer{primaryWriter}

	for _, store := range f.additional {
		w, err := store.Writer(ctx, opts...)
		if err != nil {
			// Best effort: if fallback store fails to open writer (e.g., already exists), just skip it.
			continue
		}
		writers = append(writers, w)
	}

	return newMultiWriter(writers), nil
}

// multiWriter implements content.Writer and broadcasts operations to all Writers.
type multiWriter struct {
	writers []content.Writer
}

func newMultiWriter(writers []content.Writer) content.Writer {
	if len(writers) == 1 {
		return writers[0]
	}
	return &multiWriter{writers: writers}
}

func (m *multiWriter) Write(p []byte) (n int, err error) {
	for i, w := range m.writers {
		nw, werr := w.Write(p)
		if werr != nil {
			return nw, werr
		}
		if i == 0 {
			n = nw // return the bytes written by the primary writer
		}
	}
	return n, nil
}

func (m *multiWriter) Close() error {
	var firstErr error
	for _, w := range m.writers {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *multiWriter) Digest() digest.Digest {
	return m.writers[0].Digest()
}

func (m *multiWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	var firstErr error
	for _, w := range m.writers {
		if err := w.Commit(ctx, size, expected, opts...); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *multiWriter) Status() (content.Status, error) {
	return m.writers[0].Status()
}

func (m *multiWriter) Truncate(size int64) error {
	var firstErr error
	for _, w := range m.writers {
		if err := w.Truncate(size); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
