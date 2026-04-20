package reactdag

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// workgroup — minimal errgroup-equivalent (stdlib only)
// ---------------------------------------------------------------------------

// workgroup runs goroutines and collects the first non-nil error.
// Cancelling the context does not automatically cancel the workgroup; callers
// pass a derived cancellable context themselves.
type workgroup struct {
	cancel func()
	wg     sync.WaitGroup
	mu     sync.Mutex
	err    error
}

// newWorkgroup creates a workgroup whose context is derived from ctx.
// The returned context is cancelled when the first goroutine returns an error
// or when all goroutines complete.
func newWorkgroup(ctx context.Context) (*workgroup, context.Context) {
	derivedCtx, cancel := context.WithCancel(ctx)
	return &workgroup{cancel: cancel}, derivedCtx
}

// Go launches fn in a new goroutine. If fn returns a non-nil error and it is
// the first error seen, the workgroup context is cancelled.
func (g *workgroup) Go(fn func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := fn(); err != nil {
			g.mu.Lock()
			if g.err == nil {
				g.err = err
				g.cancel()
			}
			g.mu.Unlock()
		}
	}()
}

// Wait blocks until all goroutines have returned, then cancels the context
// and returns the first error encountered (or nil).
func (g *workgroup) Wait() error {
	g.wg.Wait()
	g.cancel()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}
