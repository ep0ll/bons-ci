package cache

import (
	"context"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Combined fans out operations across multiple cache stores, querying all
// concurrently and preferring the primary store for writes. This mirrors
// BuildKit's combinedCacheManager pattern.
//
// Read strategy: probe all stores concurrently; first hit wins.
// Write strategy: write to primary only.
// Cache warming: on a non-primary hit, the result is written back to the
// primary store (best-effort, non-fatal).
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
	return &Combined{primary: primary, stores: all}
}

// Probe checks all stores concurrently and returns the first hit. If a hit
// is found in a non-primary store it is warmed into the primary store.
func (c *Combined) Probe(ctx context.Context, key Key) (string, bool, error) {
	type result struct {
		id        string
		fromStore Store
	}

	// Channel-based fan-out: first response wins via select.
	results := make(chan result, len(c.stores))
	errs := make(chan error, len(c.stores))

	// FIX: capture store explicitly per goroutine (avoids loop-variable capture
	// bug on Go ≤ 1.21; harmless on Go 1.22+ where each iteration gets its own
	// variable, but explicit capture is always correct).
	for _, s := range c.stores {
		s := s // explicit per-iteration capture
		go func() {
			id, found, err := s.Probe(ctx, key)
			if err != nil {
				errs <- err
				return
			}
			if found {
				results <- result{id: id, fromStore: s}
			} else {
				results <- result{} // empty = miss from this store
			}
		}()
	}

	// Collect up to len(stores) responses; return the first non-empty hit.
	var firstErr error
	var firstHit *result
	for range c.stores {
		select {
		case res := <-results:
			if res.id != "" && firstHit == nil {
				r := res
				firstHit = &r
			}
		case err := <-errs:
			if firstErr == nil {
				firstErr = err
			}
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}

	if firstHit == nil {
		return "", false, firstErr // miss (or error, whichever applies)
	}

	// Warm primary cache if hit came from a secondary store.
	if firstHit.fromStore != c.primary {
		// Best-effort: do not propagate warm errors.
		_ = c.primary.Save(ctx, key, firstHit.id, 0)
	}

	return firstHit.id, true, nil
}

// Save writes to the primary store only.
func (c *Combined) Save(ctx context.Context, key Key, resultID string, size int) error {
	return c.primary.Save(ctx, key, resultID, size)
}

// Load returns the Record from the first store that has it, preferring primary.
func (c *Combined) Load(ctx context.Context, key Key) (Record, error) {
	// Try primary first (no need to fan-out if primary has it).
	if rec, err := c.primary.Load(ctx, key); err == nil {
		return rec, nil
	}

	// Fan-out to additional stores concurrently.
	type loadResult struct {
		rec Record
		err error
	}
	ch := make(chan loadResult, len(c.stores)-1)
	for _, s := range c.stores {
		if s == c.primary {
			continue
		}
		s := s // explicit per-iteration capture
		go func() {
			rec, err := s.Load(ctx, key)
			ch <- loadResult{rec, err}
		}()
	}

	for range len(c.stores) - 1 {
		select {
		case lr := <-ch:
			if lr.err == nil {
				return lr.rec, nil
			}
		case <-ctx.Done():
			return Record{}, ctx.Err()
		}
	}
	return Record{}, &ErrNotFound{Key: key}
}

// Release removes from all stores concurrently. Returns the first error, but
// still attempts release on all stores.
func (c *Combined) Release(ctx context.Context, key Key) error {
	eg, ctx := errgroup.WithContext(ctx)
	for _, s := range c.stores {
		s := s // explicit per-iteration capture
		eg.Go(func() error { return s.Release(ctx, key) })
	}
	return eg.Wait()
}

// Walk iterates over the primary store only (it is the authoritative source of
// truth for stored results).
func (c *Combined) Walk(ctx context.Context, fn func(Record) error) error {
	return c.primary.Walk(ctx, fn)
}

// Stats returns aggregated stats across all stores.
func (c *Combined) Stats(ctx context.Context) (Stats, error) {
	var mu sync.Mutex
	var agg Stats
	eg, ctx := errgroup.WithContext(ctx)
	for _, s := range c.stores {
		s := s
		eg.Go(func() error {
			st, err := s.Stats(ctx)
			if err != nil {
				return nil // non-fatal: best-effort aggregation
			}
			mu.Lock()
			agg.Entries += st.Entries
			agg.TotalSize += st.TotalSize
			agg.Hits += st.Hits
			agg.Misses += st.Misses
			mu.Unlock()
			return nil
		})
	}
	return agg, eg.Wait()
}
