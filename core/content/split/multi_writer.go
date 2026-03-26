package split

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
)

type multiWriter struct {
	writers []content.Writer
}

// Close implements content.Writer.
func (m *multiWriter) Close() error {
	var (
		mu   sync.Mutex
		errs []error
	)
	wg, _ := errgroup.WithContext(context.Background())
	for _, w := range m.writers {
		wg.Go(func() error {
			if err := w.Close(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = wg.Wait()
	return errors.Join(errs...)
}

// Commit implements content.Writer.
func (m *multiWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	var (
		mu   sync.Mutex
		errs []error
	)
	wg, ctx := errgroup.WithContext(ctx)
	for _, w := range m.writers {
		wg.Go(func() error {
			if err := w.Commit(ctx, size, expected, opts...); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = wg.Wait()
	return errors.Join(errs...)
}

// Digest implements content.Writer.
func (m *multiWriter) Digest() digest.Digest {
	if len(m.writers) == 0 {
		return ""
	}
	return m.writers[0].Digest()
}

// Status implements content.Writer.
func (m *multiWriter) Status() (content.Status, error) {
	if len(m.writers) == 0 {
		return content.Status{}, fmt.Errorf("no writers available")
	}
	return m.writers[0].Status()
}

// Truncate implements content.Writer.
func (m *multiWriter) Truncate(size int64) error {
	var errs []error
	for _, w := range m.writers {
		if err := w.Truncate(size); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Write implements content.Writer.
// Writes to all underlying writers concurrently.
// Each goroutine gets its own return values to avoid data races.
func (m *multiWriter) Write(p []byte) (int, error) {
	var (
		mu   sync.Mutex
		errs []error
	)

	wg, _ := errgroup.WithContext(context.Background())
	for _, w := range m.writers {
		wg.Go(func() error {
			n, err := w.Write(p)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)
			} else if expected := len(p); n != expected {
				errs = append(errs, fmt.Errorf("required to write %d, wrote %d", expected, n))
			}
			return nil
		})
	}

	_ = wg.Wait()

	if len(errs) > 0 {
		return 0, errors.Join(errs...)
	}

	return len(p), nil
}

var _ content.Writer = &multiWriter{}

func NewMultiWriter(writers ...content.Writer) content.Writer {
	return &multiWriter{writers: writers}
}
