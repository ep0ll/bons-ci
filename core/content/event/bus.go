package event

import (
	"context"
	"sync"
	"sync/atomic"
)

// Bus is a thread-safe publish-subscribe event dispatcher.
//
// Handlers are invoked in the order they were registered.
// Publish blocks until all handlers return; PublishAsync returns immediately.
//
// The zero value is not usable; construct with NewBus.
type Bus struct {
	mu       sync.RWMutex
	handlers map[uint64]Handler
	seq      atomic.Uint64 // handler ID generator
}

// NewBus returns an initialised, empty Bus.
func NewBus() *Bus {
	return &Bus{handlers: make(map[uint64]Handler)}
}

// Subscribe registers h to receive all future events published to the Bus.
// It returns an unsubscribe function; calling it removes h from the Bus.
// The unsubscribe function is idempotent and safe to call from any goroutine.
func (b *Bus) Subscribe(h Handler) (unsubscribe func()) {
	id := b.seq.Add(1)

	b.mu.Lock()
	b.handlers[id] = h
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.handlers, id)
			b.mu.Unlock()
		})
	}
}

// Publish dispatches evt synchronously to every registered handler.
// It returns after all handlers have returned.
// ctx is forwarded to each handler as-is.
func (b *Bus) Publish(ctx context.Context, evt Event) {
	for _, h := range b.snapshot() {
		h(ctx, evt)
	}
}

// PublishAsync dispatches evt to every currently registered handler in a new
// goroutine, returning to the caller immediately. The background goroutine
// inherits a copy of ctx.
func (b *Bus) PublishAsync(ctx context.Context, evt Event) {
	handlers := b.snapshot()
	if len(handlers) == 0 {
		return
	}
	go func() {
		for _, h := range handlers {
			h(ctx, evt)
		}
	}()
}

// snapshot returns a stable copy of the current handler slice under a
// read-lock, preventing handlers from changing while we iterate.
func (b *Bus) snapshot() []Handler {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.handlers) == 0 {
		return nil
	}
	out := make([]Handler, 0, len(b.handlers))
	for _, h := range b.handlers {
		out = append(out, h)
	}
	return out
}
