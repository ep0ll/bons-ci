package cache

import (
	"context"
	"sync"
)

// Combined fans out cache operations across multiple Store backends.
// On Probe, it queries all backends concurrently and warms the primary
// on non-primary hits. This matches BuildKit's combined cache pattern.
type Combined struct {
	primary    Store
	secondaries []Store
}

// NewCombined creates a combined cache store. The first argument is the primary
// store; subsequent stores are secondaries that are probed concurrently.
func NewCombined(primary Store, secondaries ...Store) *Combined {
	return &Combined{
		primary:     primary,
		secondaries: secondaries,
	}
}

func (c *Combined) Probe(ctx context.Context, key Key) (string, bool, error) {
	// Try primary first (fast path).
	resultID, found, err := c.primary.Probe(ctx, key)
	if err != nil {
		return "", false, err
	}
	if found {
		return resultID, true, nil
	}

	// Fan-out to secondaries.
	type probeResult struct {
		resultID string
		found    bool
		err      error
	}

	results := make(chan probeResult, len(c.secondaries))
	var wg sync.WaitGroup
	for _, s := range c.secondaries {
		wg.Add(1)
		go func(store Store) {
			defer wg.Done()
			id, found, err := store.Probe(ctx, key)
			results <- probeResult{id, found, err}
		}(s)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for pr := range results {
		if pr.err != nil {
			continue
		}
		if pr.found {
			// Warm primary.
			_ = c.primary.Save(ctx, key, pr.resultID, 0)
			return pr.resultID, true, nil
		}
	}
	return "", false, nil
}

func (c *Combined) Save(ctx context.Context, key Key, resultID string, size int) error {
	return c.primary.Save(ctx, key, resultID, size)
}

func (c *Combined) Load(ctx context.Context, key Key) (*Record, error) {
	return c.primary.Load(ctx, key)
}

func (c *Combined) Release(ctx context.Context, key Key) error {
	return c.primary.Release(ctx, key)
}
