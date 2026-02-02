package content

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
)

type multiWriter struct {
	writers []content.Writer
}

// Close implements content.Writer.
func (m *multiWriter) Close() error {
	wg, _ := errgroup.WithContext(context.Background())
	for _, w := range m.writers {
		wg.Go(w.Close)
	}
	return wg.Wait()
}

// Commit implements content.Writer.
func (m *multiWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	wg, ctx := errgroup.WithContext(ctx)
	for _, w := range m.writers {
		wg.Go(func() error {
			return w.Commit(ctx, size, expected, opts...)
		})
	}
	return wg.Wait()
}

// Digest implements content.Writer.
func (m *multiWriter) Digest() digest.Digest {
	return m.writers[0].Digest()
}

// Status implements content.Writer.
func (m *multiWriter) Status() (content.Status, error) {
	return m.writers[0].Status()
}

// Truncate implements content.Writer.
func (m *multiWriter) Truncate(size int64) error {
	for _, w := range m.writers {
		if err := w.Truncate(size); err != nil {
			return err
		}
	}
	return nil
}

// Write implements content.Writer.
func (m *multiWriter) Write(p []byte) (n int, err error) {
	wg, _ := errgroup.WithContext(context.Background())
	for _, w := range m.writers {
		wg.Go(func() error {
			n, err = w.Write(p)
			if m := len(p); err == nil && m != n {
				return fmt.Errorf("required to write %d, wrote %d", m, n)
			}
			return err
		})
	}
	return n, wg.Wait()
}

var _ content.Writer = &multiWriter{}
