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
		wg   sync.WaitGroup
	)
	wg.Add(len(m.stores))
	for _, cs := range m.stores {
		go func(cs content.Store) {
			defer wg.Done()
			if err := cs.Abort(ctx, ref); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(cs)
	}
	wg.Wait()
	return filterNotFoundErrors(errs, len(m.stores))
}

// Delete implements content.Store.
func (m *multiContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	wg.Add(len(m.stores))
	for _, cs := range m.stores {
		go func(cs content.Store) {
			defer wg.Done()
			if err := cs.Delete(ctx, dgst); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(cs)
	}
	wg.Wait()
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
		errs     []error
		seenRefs = make(map[string]struct{})
		wg       sync.WaitGroup
	)

	wg.Add(len(m.stores))
	for _, cs := range m.stores {
		go func(cs content.Store) {
			defer wg.Done()
			statuses, err := cs.ListStatuses(ctx, filters...)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for _, st := range statuses {
				if _, ok := seenRefs[st.Ref]; !ok {
					seenRefs[st.Ref] = struct{}{}
					result = append(result, st)
				}
			}
		}(cs)
	}
	wg.Wait()

	if len(errs) == len(m.stores) && len(m.stores) > 0 {
		return nil, errors.Join(errs...)
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
		wg       sync.WaitGroup
	)

	wg.Add(len(m.stores))
	for _, cs := range m.stores {
		go func(cs content.Store) {
			defer wg.Done()
			upd, err := cs.Update(ctx, info, fieldpaths...)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else {
				lastInfo = upd
				updated = true
			}
		}(cs)
	}
	wg.Wait()

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
	var (
		wg    sync.WaitGroup
		errs  []error
		mu    sync.Mutex
		infos sync.Map
	)
	
	wg.Add(len(m.stores))
	for _, cs := range m.stores {
		go func(cs content.Store) {
			defer wg.Done()
			err := cs.Walk(ctx, func(i content.Info) error {
				_, loaded := infos.LoadOrStore(i.Digest, i)
				if !loaded {
					mu.Lock()
					defer mu.Unlock()
					return fn(i) // Serialize access to WalkFunc
				}
				return nil
			}, filters...)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(cs)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Writer implements content.Store.
func (m *multiContentStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var (
		mu      sync.Mutex
		writers []content.Writer
		errs    []error
		wg      sync.WaitGroup
	)

	wg.Add(len(m.stores))
	for _, cs := range m.stores {
		go func(cs content.Store) {
			defer wg.Done()
			writer, err := cs.Writer(ctx, opts...)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else {
				writers = append(writers, writer)
			}
		}(cs)
	}
	wg.Wait()

	if len(writers) == 0 {
		return nil, filterNotFoundErrors(errs, len(m.stores))
	}

	return split.NewMultiWriter(writers...), nil
}

var _ content.Store = &multiContentStore{}
