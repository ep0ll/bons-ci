package dagstore

import (
	"context"
	"sync"
)

// WorkerPool bounds concurrent goroutines. Safe for concurrent use.
type WorkerPool struct {
	sem chan struct{}
}

// NewWorkerPool creates a pool capped at n concurrent goroutines. Panics if n < 1.
func NewWorkerPool(n int) *WorkerPool {
	if n < 1 {
		panic("dagstore: WorkerPool size must be >= 1")
	}
	return &WorkerPool{sem: make(chan struct{}, n)}
}

// Go acquires a slot (blocking until available or ctx cancelled) then runs fn
// synchronously in the caller's goroutine.
func (p *WorkerPool) Go(ctx context.Context, fn func() error) error {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.sem }()
	return fn()
}

// RunAll fans tasks out concurrently over the pool and waits for all to finish.
// Returns the first non-nil error; all tasks are always launched.
func (p *WorkerPool) RunAll(ctx context.Context, tasks []func() error) error {
	if len(tasks) == 0 {
		return nil
	}

	errCh := make(chan error, len(tasks))
	var wg sync.WaitGroup
	wg.Add(len(tasks))

	for _, task := range tasks {
		task := task
		go func() {
			defer wg.Done()
			// acquire slot
			select {
			case p.sem <- struct{}{}:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
			err := task()
			<-p.sem
			if err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	// Return first error seen (channel is buffered so no blocking).
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// Size returns the maximum concurrency of the pool.
func (p *WorkerPool) Size() int { return cap(p.sem) }
