package split

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

type multiWriter struct {
	writers []content.Writer
}

// Close implements content.Writer.
func (m *multiWriter) Close() error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	wg.Add(len(m.writers))
	for _, w := range m.writers {
		go func(w content.Writer) {
			defer wg.Done()
			if err := w.Close(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Commit implements content.Writer.
func (m *multiWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	wg.Add(len(m.writers))
	for _, w := range m.writers {
		go func(w content.Writer) {
			defer wg.Done()
			if err := w.Commit(ctx, size, expected, opts...); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
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
// Writes to all underlying writers sequentially. This prevents catastrophic
// overhead from spawning goroutines for every byte slice while ensuring
// strictly correct byte sequence delivery.
func (m *multiWriter) Write(p []byte) (int, error) {
	for _, w := range m.writers {
		n, err := w.Write(p)
		if err != nil {
			return n, err
		}
		if n != len(p) {
			return n, fmt.Errorf("required to write %d, wrote %d", len(p), n)
		}
	}
	return len(p), nil
}

var _ content.Writer = &multiWriter{}

func NewMultiWriter(writers ...content.Writer) content.Writer {
	return &multiWriter{writers: writers}
}
