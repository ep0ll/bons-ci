// Package fanout provides a content.Store that fans out every operation to a
// set of backing stores concurrently.
//
// Read semantics — first success wins:
//   - Info, ReaderAt, Status — returns the result of the first store to succeed.
//     If all stores return not-found, errdefs.ErrNotFound is returned.
//
// Write semantics — fan-out with partial tolerance:
//   - Writer — collects writers from all stores; returns an error only if no
//     store could produce a writer.
//   - Delete, Abort, Update — run on all stores concurrently; not-found errors
//     from individual stores are filtered out unless every store returned one.
//   - Walk — deduplicates by digest across stores; fn is called once per digest.
//   - ListStatuses — deduplicates by Ref across stores.
package fanout

import (
	"context"
	"errors"
	"sync"

	writerfanout "github.com/bons/bons-ci/core/content/writer/fanout"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// New returns a content.Store that fans out every operation to all provided
// stores. At least one store must be provided.
func New(stores ...content.Store) content.Store {
	return &store{stores: stores}
}

type store struct {
	stores []content.Store
}

// Info returns Info from the first store that succeeds.
// Returns errdefs.ErrNotFound if all stores return not-found.
func (s *store) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	for _, cs := range s.stores {
		if info, err := cs.Info(ctx, dgst); err == nil {
			return info, nil
		}
	}
	return content.Info{}, errdefs.ErrNotFound
}

// ReaderAt returns a ReaderAt from the first store that succeeds.
func (s *store) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	for _, cs := range s.stores {
		if r, err := cs.ReaderAt(ctx, desc); err == nil {
			return r, nil
		}
	}
	return nil, errdefs.ErrNotFound
}

// Status returns the write status from the first store that has it.
func (s *store) Status(ctx context.Context, ref string) (content.Status, error) {
	for _, cs := range s.stores {
		if st, err := cs.Status(ctx, ref); err == nil {
			return st, nil
		}
	}
	return content.Status{}, errdefs.ErrNotFound
}

// Delete deletes dgst from all stores concurrently.
func (s *store) Delete(ctx context.Context, dgst digest.Digest) error {
	return s.parallel(func(cs content.Store) error {
		return cs.Delete(ctx, dgst)
	})
}

// Abort aborts the in-progress write ref in all stores concurrently.
func (s *store) Abort(ctx context.Context, ref string) error {
	return s.parallel(func(cs content.Store) error {
		return cs.Abort(ctx, ref)
	})
}

// Update updates content metadata across all stores concurrently.
// Returns the last successful update result; any non-not-found errors are joined.
func (s *store) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	type result struct {
		info content.Info
		err  error
	}

	results := make([]result, len(s.stores))
	var wg sync.WaitGroup
	wg.Add(len(s.stores))

	for i, cs := range s.stores {
		go func(i int, cs content.Store) {
			defer wg.Done()
			updated, err := cs.Update(ctx, info, fieldpaths...)
			results[i] = result{info: updated, err: err}
		}(i, cs)
	}
	wg.Wait()

	var (
		lastInfo content.Info
		errs     []error
		updated  bool
	)
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
		} else {
			lastInfo = r.info
			updated = true
		}
	}

	if updated {
		return lastInfo, filterNotFound(errs, len(s.stores))
	}
	return content.Info{}, filterNotFound(errs, len(s.stores))
}

// ListStatuses aggregates in-progress write statuses from all stores,
// deduplicating by Ref.
func (s *store) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	type partialResult struct {
		statuses []content.Status
		err      error
	}

	results := make([]partialResult, len(s.stores))
	var wg sync.WaitGroup
	wg.Add(len(s.stores))

	for i, cs := range s.stores {
		go func(i int, cs content.Store) {
			defer wg.Done()
			statuses, err := cs.ListStatuses(ctx, filters...)
			results[i] = partialResult{statuses: statuses, err: err}
		}(i, cs)
	}
	wg.Wait()

	seen := make(map[string]struct{})
	var (
		all  []content.Status
		errs []error
	)
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for _, st := range r.statuses {
			if _, ok := seen[st.Ref]; !ok {
				seen[st.Ref] = struct{}{}
				all = append(all, st)
			}
		}
	}

	if len(errs) == len(s.stores) && len(s.stores) > 0 {
		return nil, errors.Join(errs...)
	}
	return all, nil
}

// Walk iterates over content in all stores concurrently, calling fn once per
// unique digest. fn is always invoked serially, regardless of how many stores
// are walked concurrently.
func (s *store) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	var (
		seen sync.Map
		mu   sync.Mutex // serialises calls to fn
		errs []error
		wg   sync.WaitGroup
	)

	wg.Add(len(s.stores))
	for _, cs := range s.stores {
		go func(cs content.Store) {
			defer wg.Done()
			err := cs.Walk(ctx, func(info content.Info) error {
				if _, loaded := seen.LoadOrStore(info.Digest, struct{}{}); loaded {
					return nil // already visited
				}
				mu.Lock()
				defer mu.Unlock()
				return fn(info)
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

// Writer collects writers from all stores concurrently and returns a fanout
// writer over those that succeeded. Returns an error only if no store
// produced a writer.
func (s *store) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	type result struct {
		w   content.Writer
		err error
	}

	results := make([]result, len(s.stores))
	var wg sync.WaitGroup
	wg.Add(len(s.stores))

	for i, cs := range s.stores {
		go func(i int, cs content.Store) {
			defer wg.Done()
			w, err := cs.Writer(ctx, opts...)
			results[i] = result{w: w, err: err}
		}(i, cs)
	}
	wg.Wait()

	var (
		writers []content.Writer
		errs    []error
	)
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
		} else {
			writers = append(writers, r.w)
		}
	}

	if len(writers) == 0 {
		return nil, filterNotFound(errs, len(s.stores))
	}
	return writerfanout.New(writers...), nil
}

// parallel runs fn against every store concurrently and returns a joined error,
// filtering not-found errors intelligently.
func (s *store) parallel(fn func(content.Store) error) error {
	errs := make([]error, len(s.stores))
	var wg sync.WaitGroup
	wg.Add(len(s.stores))

	for i, cs := range s.stores {
		go func(i int, cs content.Store) {
			defer wg.Done()
			errs[i] = fn(cs)
		}(i, cs)
	}
	wg.Wait()

	var nonNil []error
	for _, err := range errs {
		if err != nil {
			nonNil = append(nonNil, err)
		}
	}
	return filterNotFound(nonNil, len(s.stores))
}

// filterNotFound returns nil if all errors are not-found (and at least one
// succeeded). If every one of total operations returned not-found, it returns
// errdefs.ErrNotFound. Otherwise it joins non-not-found errors.
func filterNotFound(errs []error, total int) error {
	if len(errs) == 0 {
		return nil
	}

	var real []error
	notFoundCount := 0

	for _, err := range errs {
		if errdefs.IsNotFound(err) {
			notFoundCount++
		} else {
			real = append(real, err)
		}
	}

	if len(real) == 0 {
		// All errors are not-found.
		if notFoundCount == total {
			return errdefs.ErrNotFound // every store missed
		}
		return nil // at least one succeeded
	}
	return errors.Join(real...)
}

var _ content.Store = (*store)(nil)
