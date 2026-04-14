// Package fanout provides a content.Writer that broadcasts every write
// operation to a set of underlying writers.
package fanout

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

// New returns a content.Writer that fans out every operation to each of the
// provided writers.
//
// Digest and Status delegate to the first writer (considered authoritative).
// Write is performed sequentially across all writers to preserve byte-order
// semantics; all other operations (Commit, Close, Truncate) run concurrently.
//
// If writers is empty, the returned writer behaves as a no-op that returns
// sentinel errors from Digest and Status.
func New(writers ...content.Writer) content.Writer {
	return &fanoutWriter{writers: writers}
}

type fanoutWriter struct {
	writers []content.Writer
}

// Write delivers p to every underlying writer in order.
// It returns on the first error, preserving the invariant that all writers
// have received exactly the same byte sequence up to the point of failure.
func (f *fanoutWriter) Write(p []byte) (int, error) {
	for _, w := range f.writers {
		n, err := w.Write(p)
		if err != nil {
			return n, fmt.Errorf("fanout write: %w", err)
		}
		if n != len(p) {
			return n, fmt.Errorf("fanout write: short write: wrote %d of %d bytes", n, len(p))
		}
	}
	return len(p), nil
}

// Commit commits all writers concurrently and joins any errors.
func (f *fanoutWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	return f.fanout(func(w content.Writer) error {
		return w.Commit(ctx, size, expected, opts...)
	})
}

// Truncate truncates all writers sequentially.
func (f *fanoutWriter) Truncate(size int64) error {
	var errs []error
	for _, w := range f.writers {
		if err := w.Truncate(size); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close closes all writers concurrently and joins any errors.
func (f *fanoutWriter) Close() error {
	return f.fanout(func(w content.Writer) error {
		return w.Close()
	})
}

// Digest returns the current digest from the first (authoritative) writer.
// Returns an empty digest if there are no writers.
func (f *fanoutWriter) Digest() digest.Digest {
	if len(f.writers) == 0 {
		return digest.Digest("")
	}
	return f.writers[0].Digest()
}

// Status returns the write status from the first (authoritative) writer.
// Returns an error if there are no writers.
func (f *fanoutWriter) Status() (content.Status, error) {
	if len(f.writers) == 0 {
		return content.Status{}, fmt.Errorf("fanout: no writers configured")
	}
	return f.writers[0].Status()
}

// fanout runs fn against every writer concurrently and joins all errors.
func (f *fanoutWriter) fanout(fn func(content.Writer) error) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	wg.Add(len(f.writers))
	for _, w := range f.writers {
		go func(w content.Writer) {
			defer wg.Done()
			if err := fn(w); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	return errors.Join(errs...)
}

var _ content.Writer = (*fanoutWriter)(nil)
