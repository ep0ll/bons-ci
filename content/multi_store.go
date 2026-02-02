package content

import (
	"context"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

type multiContentStore struct {
	stores []content.Store
}

// Abort implements content.Store.
func (m *multiContentStore) Abort(ctx context.Context, ref string) error {
	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			return cs.Abort(ctx, ref)
		})
	}
	return wg.Wait()
}

// Delete implements content.Store.
func (m *multiContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			return cs.Delete(ctx, dgst)
		})
	}
	return wg.Wait()
}

// Info implements content.Store.
func (m *multiContentStore) Info(ctx context.Context, dgst digest.Digest) (info content.Info, err error) {
	for _, cs := range m.stores {
		if info, err = cs.Info(ctx, dgst); err == nil {
			return info, nil
		}
	}

	return
}

// ListStatuses implements content.Store.
func (m *multiContentStore) ListStatuses(ctx context.Context, filters ...string) (status []content.Status, err error) {
	for _, cs := range m.stores {
		if status, err = cs.ListStatuses(ctx, filters...); err == nil {
			return status, nil
		}
	}

	return
}

// ReaderAt implements content.Store.
func (m *multiContentStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (reader content.ReaderAt, err error) {
	for _, cs := range m.stores {
		if reader, err = cs.ReaderAt(ctx, desc); err == nil {
			return reader, nil
		}
	}

	return
}

// Status implements content.Store.
func (m *multiContentStore) Status(ctx context.Context, ref string) (status content.Status, err error) {
	for _, cs := range m.stores {
		if status, err = cs.Status(ctx, ref); err == nil {
			return status, nil
		}
	}

	return
}

// Update implements content.Store.
func (m *multiContentStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (cinfo content.Info, err error) {
	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			info, err := cs.Update(ctx, info, fieldpaths...)
			if err == nil {
				cinfo = info
			}

			return err
		})
	}
	return info, wg.Wait()
}

// Walk implements content.Store.
func (m *multiContentStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	wg, ctx := errgroup.WithContext(ctx)
	infos := sync.Map{}
	for _, cs := range m.stores {
		wg.Go(func() error {
			return cs.Walk(ctx, func(i content.Info) error {
				_, loaded := infos.LoadOrStore(i.Digest, i)
				if !loaded {
					return fn(i)
				}

				return nil
			}, filters...)
		})
	}
	return wg.Wait()
}

// Writer implements content.Store.
func (m *multiContentStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	wg, ctx := errgroup.WithContext(ctx)
	var writers []content.Writer
	for _, cs := range m.stores {
		wg.Go(func() error {
			writer, err := cs.Writer(ctx, opts...)
			if err == nil {
				writers = append(writers, writer)
			}

			return err
		})
	}
	return &multiWriter{writers: writers}, wg.Wait()
}

var _ content.Store = &multiContentStore{}
