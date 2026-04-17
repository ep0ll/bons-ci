// Package reactive provides a lightweight, type-safe pub/sub event bus and
// reactive observable primitive for propagating graph mutations.
package reactive

import (
	"sync"
	"sync/atomic"
)

// ─── EventBus ─────────────────────────────────────────────────────────────────

// EventBus is a typed concurrent publish/subscribe hub.
// Multiple goroutines may publish and subscribe concurrently.
// Publish is non-blocking: a subscriber whose buffer is full will miss the event
// rather than stalling the publisher.
type EventBus[T any] struct {
	mu   sync.RWMutex
	subs map[uint64]*subscription[T]
	seq  atomic.Uint64
}

// NewEventBus creates an empty EventBus.
func NewEventBus[T any]() *EventBus[T] {
	return &EventBus[T]{subs: make(map[uint64]*subscription[T])}
}

// Subscribe registers handler to receive events. Returns a Subscription whose
// Cancel method de-registers it.
func (b *EventBus[T]) Subscribe(handler func(T)) Subscription {
	id := b.seq.Add(1)
	s := &subscription[T]{
		id:      id,
		ch:      make(chan T, 128),
		bus:     b,
		handler: handler,
		done:    make(chan struct{}),
	}
	b.mu.Lock()
	b.subs[id] = s
	b.mu.Unlock()
	go s.drain()
	return s
}

// Publish delivers event to all current subscribers without blocking.
func (b *EventBus[T]) Publish(event T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		select {
		case s.ch <- event:
		default:
		}
	}
}

// Close removes all subscribers.
func (b *EventBus[T]) Close() {
	b.mu.Lock()
	subs := b.subs
	b.subs = make(map[uint64]*subscription[T])
	b.mu.Unlock()
	for _, s := range subs {
		s.close()
	}
}

type subscription[T any] struct {
	id      uint64
	ch      chan T
	bus     *EventBus[T]
	handler func(T)
	done    chan struct{}
	once    sync.Once
}

func (s *subscription[T]) drain() {
	for {
		select {
		case event, ok := <-s.ch:
			if !ok {
				return
			}
			s.handler(event)
		case <-s.done:
			return
		}
	}
}

func (s *subscription[T]) close() {
	s.once.Do(func() { close(s.done) })
}

func (s *subscription[T]) Cancel() {
	s.bus.mu.Lock()
	delete(s.bus.subs, s.id)
	s.bus.mu.Unlock()
	s.close()
}

// ─── Subscription ─────────────────────────────────────────────────────────────

// Subscription represents an active event subscription.
type Subscription interface {
	Cancel()
}

// ─── Observable ───────────────────────────────────────────────────────────────

// Observable holds a current value and notifies subscribers on change.
// It is safe for concurrent access.
type Observable[T comparable] struct {
	mu      sync.RWMutex
	current T
	bus     *EventBus[ChangeEvent[T]]
}

// ChangeEvent carries old and new values.
type ChangeEvent[T any] struct {
	Old T
	New T
}

// NewObservable creates an Observable with the given initial value.
func NewObservable[T comparable](initial T) *Observable[T] {
	return &Observable[T]{current: initial, bus: NewEventBus[ChangeEvent[T]]()}
}

// Value returns the current value.
func (o *Observable[T]) Value() T {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.current
}

// Set updates the value, firing subscribers only if it changed.
func (o *Observable[T]) Set(value T) {
	o.mu.Lock()
	old := o.current
	changed := old != value
	if changed {
		o.current = value
	}
	o.mu.Unlock()
	if changed {
		o.bus.Publish(ChangeEvent[T]{Old: old, New: value})
	}
}

// Subscribe registers a handler for changes.
func (o *Observable[T]) Subscribe(handler func(ChangeEvent[T])) Subscription {
	return o.bus.Subscribe(handler)
}

// Close shuts down the underlying bus.
func (o *Observable[T]) Close() { o.bus.Close() }
