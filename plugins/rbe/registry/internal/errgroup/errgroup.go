// Package errgroup provides a simple errgroup implementation using only
// the standard library, replacing golang.org/x/sync/errgroup so the project
// compiles without external network dependencies.
package errgroup

import (
	"context"
	"sync"
)

// Group runs a collection of goroutines and collects their errors.
// It is a minimal replacement for golang.org/x/sync/errgroup.Group.
type Group struct {
	cancel func()
	wg     sync.WaitGroup
	mu     sync.Mutex
	err    error
}

// WithContext returns a Group and a derived Context that is cancelled when
// the first goroutine in the group returns a non-nil error, or when Wait
// returns, whichever occurs first.
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{cancel: cancel}, ctx
}

// Go calls f in a new goroutine. The first non-nil error from f is
// preserved and returned by Wait. After the first error the context
// passed to WithContext is cancelled.
func (g *Group) Go(f func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := f(); err != nil {
			g.mu.Lock()
			if g.err == nil {
				g.err = err
				if g.cancel != nil {
					g.cancel()
				}
			}
			g.mu.Unlock()
		}
	}()
}

// Wait blocks until all goroutines launched by Go have completed.
// It returns the first non-nil error (if any).
func (g *Group) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel()
	}
	return g.err
}
