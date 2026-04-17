package cache

import (
	"context"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Combined fans out operations across multiple cache stores, querying all
// concurrently and preferring the primary store for writes. This mirrors
// BuildKit's combinedCacheManager pattern.
type Combined struct {
	primary Store
	stores  []Store
}

// NewCombined creates a combined cache store. The primary store is used for
// writes; all stores are probed concurrently for reads.
func NewCombined(primary Store, additional ...Store) *Combined {
	all := make([]Store, 0, 1+len(additional))
	all = append(all, primary)
	all = append(all, additional...)
	return &Combined{
		primary: primary,
		stores:  all,
	}
}

// Probe checks all stores concurrently and returns the first hit.
// If the result is found in a non-primary store, it is saved to the
// primary store for future lookups (cache warming).
func (c *Combined) Probe(ctx context.Context, key Key) (string, bool, error) {
	type probeResult struct {
		resultID string
		isPrimary bool
	}

	results := make([]probeResult, len(c.stores))
	eg, ctx := errgroup.WithContext(ctx)

	for i, s := range c.stores {
		eg.Go(func() error {
			resultID, found, err := s.Probe(ctx, key)
			if err != nil || !found {
				return err
			}
			results[i] = probeResult{resultID: resultID, isPrimary: s == c.primary}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return "", false, err
	}

	// Prefer primary store result.
	for _, r := range results {
		if r.resultID != "" && r.isPrimary {
			return r.resultID, true, nil
		}
	}

	// Use any non-primary result and warm the primary cache.
	for _, r := range results {
		if r.resultID != "" {
			// Best-effort cache warming — don't fail the probe.
			_ = c.primary.Save(ctx, key, r.resultID, 0)
			return r.resultID, true, nil
		}
	}

	return "", false, nil
}

// Save writes to the primary store only.
func (c *Combined) Save(ctx context.Context, key Key, resultID string, size int) error {
	return c.primary.Save(ctx, key, resultID, size)
}

// Load loads from the first store that has the entry.
func (c *Combined) Load(ctx context.Context, key Key) (Record, error) {
	type loadResult struct {
		rec Record
		err error
	}

	results := make([]loadResult, len(c.stores))
	eg, ctx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	var found bool

	for i, s := range c.stores {
		eg.Go(func() error {
			rec, err := s.Load(ctx, key)
			if err != nil {
				results[i] = loadResult{err: err}
				return nil // Don't fail other goroutines.
			}
			mu.Lock()
			found = true
			results[i] = loadResult{rec: rec}
			mu.Unlock()
			return nil
		})
	}

	_ = eg.Wait()
	if !found {
		return Record{}, &ErrNotFound{Key: key}
	}

	// Prefer primary.
	for i, r := range results {
		if r.err == nil && c.stores[i] == c.primary {
			return r.rec, nil
		}
	}
	for _, r := range results {
		if r.err == nil {
			return r.rec, nil
		}
	}
	return Record{}, &ErrNotFound{Key: key}
}

// Release removes from all stores concurrently.
func (c *Combined) Release(ctx context.Context, key Key) error {
	eg, ctx := errgroup.WithContext(ctx)
	for _, s := range c.stores {
		eg.Go(func() error {
			return s.Release(ctx, key)
		})
	}
	return eg.Wait()
}

// Walk iterates over the primary store only.
func (c *Combined) Walk(ctx context.Context, fn func(Record) error) error {
	return c.primary.Walk(ctx, fn)
}
