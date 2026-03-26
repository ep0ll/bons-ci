package content

import (
	"context"
	"errors"
	"sync"

	"github.com/bons/bons-ci/core/content/split"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

func filterNotFoundErrors(errs []error, total int) error {
	if len(errs) == 0 {
		return nil
	}
	allNotFound := true
	var filtered []error
	for _, err := range errs {
		if errdefs.IsNotFound(err) {
			continue
		}
		allNotFound = false
		filtered = append(filtered, err)
	}

	if allNotFound {
		if len(errs) == total {
			return errdefs.ErrNotFound
		}
		return nil
	}
	return errors.Join(filtered...)
}

type multiContentStore struct {
	stores []content.Store
}

// Abort implements content.Store.
func (m *multiContentStore) Abort(ctx context.Context, ref string) error {
	var (
		mu   sync.Mutex
		errs []error
	)
	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			if err := cs.Abort(ctx, ref); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = wg.Wait()
	return filterNotFoundErrors(errs, len(m.stores))
}

// Delete implements content.Store.
func (m *multiContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	var (
		mu   sync.Mutex
		errs []error
	)
	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			if err := cs.Delete(ctx, dgst); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = wg.Wait()
	return filterNotFoundErrors(errs, len(m.stores))
}

// Info implements content.Store.
func (m *multiContentStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	for _, cs := range m.stores {
		if info, err := cs.Info(ctx, dgst); err == nil {
			return info, nil
		}
	}

	return content.Info{}, errdefs.ErrNotFound
}

// ListStatuses implements content.Store.
// Aggregates statuses from all stores, deduplicating by Ref.
func (m *multiContentStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	var (
		mu       sync.Mutex
		result   []content.Status
		seenRefs = make(map[string]struct{})
	)

	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			statuses, err := cs.ListStatuses(ctx, filters...)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for _, st := range statuses {
				if _, ok := seenRefs[st.Ref]; !ok {
					seenRefs[st.Ref] = struct{}{}
					result = append(result, st)
				}
			}
			return nil
		})
	}

	if err := wg.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}

// ReaderAt implements content.Store.
func (m *multiContentStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	for _, cs := range m.stores {
		if reader, err := cs.ReaderAt(ctx, desc); err == nil {
			return reader, nil
		}
	}

	return nil, errdefs.ErrNotFound
}

// Status implements content.Store.
func (m *multiContentStore) Status(ctx context.Context, ref string) (content.Status, error) {
	for _, cs := range m.stores {
		if status, err := cs.Status(ctx, ref); err == nil {
			return status, nil
		}
	}

	return content.Status{}, errdefs.ErrNotFound
}

// Update implements content.Store.
func (m *multiContentStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	var (
		mu       sync.Mutex
		lastInfo content.Info
		errs     []error
		updated  bool
	)

	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			upd, err := cs.Update(ctx, info, fieldpaths...)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else {
				lastInfo = upd
				updated = true
			}
			return nil
		})
	}

	_ = wg.Wait()

	if updated {
		// If at least one succeeded, return any non-not-found errors joined, or just success.
		if err := filterNotFoundErrors(errs, len(m.stores)); err != nil && !errdefs.IsNotFound(err) {
			return lastInfo, err
		}
		return lastInfo, nil
	}

	return content.Info{}, filterNotFoundErrors(errs, len(m.stores))
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
	var (
		mu      sync.Mutex
		writers []content.Writer
	)

	wg, ctx := errgroup.WithContext(ctx)
	for _, cs := range m.stores {
		wg.Go(func() error {
			writer, err := cs.Writer(ctx, opts...)
			if err != nil {
				return err
			}
			mu.Lock()
			writers = append(writers, writer)
			mu.Unlock()
			return nil
		})
	}

	if err := wg.Wait(); err != nil {
		// Close any writers that were successfully opened
		for _, w := range writers {
			w.Close()
		}
		return nil, err
	}

	if len(writers) == 0 {
		return nil, errdefs.ErrNotFound
	}

	return split.NewMultiWriter(writers...), nil
}

var _ content.Store = &multiContentStore{}
