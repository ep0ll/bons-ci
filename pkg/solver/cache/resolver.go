package cache

import (
	"slices"
	"sync"
)

// ResolverCache deduplicates concurrent operations for the same key.
// When multiple goroutines try to resolve the same cache key, only one does
// the work; others wait and receive the cached result. This matches BuildKit's
// resolverCache in resolvercache.go.
type ResolverCache struct {
	mu    sync.Mutex
	locks map[any]*resolverEntry
}

type resolverEntry struct {
	waiting []chan struct{}
	values  []any
	locked  bool
}

// NewResolverCache creates a new resolver cache.
func NewResolverCache() *ResolverCache {
	return &ResolverCache{locks: make(map[any]*resolverEntry)}
}

// Lock acquires exclusive access for the given key. Returns previously
// cached values. The returned release function must be called when done,
// passing the new value to cache (or nil if nothing should be cached).
//
// If another goroutine holds the lock, this blocks until it is released.
func (r *ResolverCache) Lock(key any) (values []any, release func(any) error, err error) {
	r.mu.Lock()
	e, ok := r.locks[key]
	if !ok {
		e = &resolverEntry{}
		r.locks[key] = e
	}

	if !e.locked {
		e.locked = true
		values = slices.Clone(e.values)
		r.mu.Unlock()
		return values, func(v any) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if v != nil {
				e.values = append(e.values, v)
			}
			for _, ch := range e.waiting {
				close(ch)
			}
			e.waiting = nil
			e.locked = false
			if len(e.values) == 0 {
				delete(r.locks, key)
			}
			return nil
		}, nil
	}

	// Another goroutine holds the lock — wait.
	ch := make(chan struct{})
	e.waiting = append(e.waiting, ch)
	r.mu.Unlock()

	<-ch // blocked until release

	r.mu.Lock()
	defer r.mu.Unlock()
	e2, ok := r.locks[key]
	if !ok {
		return nil, nil, nil // key was deleted
	}
	values = slices.Clone(e2.values)
	if e2.locked {
		// Shouldn't happen; protect against logic errors.
		return values, func(any) error { return nil }, nil
	}
	e2.locked = true
	return values, func(v any) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if v != nil {
			e2.values = append(e2.values, v)
		}
		for _, ch := range e2.waiting {
			close(ch)
		}
		e2.waiting = nil
		e2.locked = false
		if len(e2.values) == 0 {
			delete(r.locks, key)
		}
		return nil
	}, nil
}

// CombinedResolverCache wraps multiple ResolverCaches. Lock() calls each
// in parallel, merges values, and returns a combined release function.
type CombinedResolverCache struct {
	caches []*ResolverCache
}

// NewCombinedResolverCache creates a combined resolver cache.
func NewCombinedResolverCache(caches ...*ResolverCache) *CombinedResolverCache {
	return &CombinedResolverCache{caches: caches}
}

// Lock acquires the lock on all underlying caches concurrently.
func (c *CombinedResolverCache) Lock(key any) (values []any, release func(any) error, err error) {
	if len(c.caches) == 0 {
		return nil, func(any) error { return nil }, nil
	}

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		allValues []any
		releasers []func(any) error
		firstErr  error
	)

	wg.Add(len(c.caches))
	for _, rc := range c.caches {
		go func(rc *ResolverCache) {
			defer wg.Done()
			vals, rel, e := rc.Lock(key)
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				if firstErr == nil {
					firstErr = e
				}
				return
			}
			allValues = append(allValues, vals...)
			releasers = append(releasers, rel)
		}(rc)
	}
	wg.Wait()

	if firstErr != nil {
		for _, r := range releasers {
			_ = r(nil)
		}
		return nil, nil, firstErr
	}

	release = func(v any) error {
		var errOnce error
		for _, r := range releasers {
			if e := r(v); e != nil && errOnce == nil {
				errOnce = e
			}
		}
		return errOnce
	}

	return allValues, release, nil
}
