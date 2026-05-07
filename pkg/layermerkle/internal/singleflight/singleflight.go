// Package singleflight provides deduplication of concurrent function calls
// and a context-aware variant where cancellation is propagated correctly.
package singleflight

import (
	"context"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Group — standard singleflight (no context)
// ─────────────────────────────────────────────────────────────────────────────

type call struct {
	wg  sync.WaitGroup
	val any
	err error
}

// Group deduplicates concurrent calls sharing the same key.
type Group struct {
	mu sync.Mutex
	m  map[string]*call
}

// Do executes fn once per key; concurrent callers share the result.
// Returns (value, error, shared) where shared=true means a coalesced result.
func (g *Group) Do(key string, fn func() (any, error)) (any, error, bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err, true
	}
	c := &call{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err, false
}

// ─────────────────────────────────────────────────────────────────────────────
// ContextGroup — context-cancellation-aware singleflight
// ─────────────────────────────────────────────────────────────────────────────

type contextCall struct {
	done chan struct{}
	val  any
	err  error
}

// ContextGroup deduplicates context-aware fn calls. If a waiting caller's ctx
// is cancelled before the in-flight call completes, that caller receives
// ctx.Err() immediately without affecting the in-flight goroutine.
type ContextGroup struct {
	mu sync.Mutex
	m  map[string]*contextCall
}

// Do executes fn(ctx) once per key; concurrent callers share the result or
// return early on context cancellation.
func (g *ContextGroup) Do(ctx context.Context, key string, fn func(ctx context.Context) (any, error)) (any, error, bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*contextCall)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.val, c.err, true
		case <-ctx.Done():
			return nil, ctx.Err(), true
		}
	}

	innerCtx, cancel := context.WithCancel(context.Background())
	c := &contextCall{done: make(chan struct{})}
	g.m[key] = c
	g.mu.Unlock()

	go func() {
		c.val, c.err = fn(innerCtx)
		cancel()
		close(c.done)
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
	}()

	// Propagate parent cancellation into innerCtx.
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-c.done:
		}
	}()

	select {
	case <-c.done:
		return c.val, c.err, false
	case <-ctx.Done():
		return nil, ctx.Err(), false
	}
}
