// Package reactive provides a lightweight, type-safe event bus and observable
// primitive for propagating graph mutations throughout the system.
//
// Design goals:
//   - Zero external dependencies beyond core.
//   - Non-blocking publish via buffered channels; slow consumers do not stall
//     the graph mutator.
//   - Subscriptions are cancellable without data races.
//   - All types are generic so the compiler enforces event-type correctness.
package reactive

import (
	"sync"
	"sync/atomic"
)

// ─── EventBus ────────────────────────────────────────────────────────────────

// EventBus is a typed, concurrent publish/subscribe hub.
// Multiple goroutines may publish and subscribe concurrently.
type EventBus[T any] struct {
	mu   sync.RWMutex
	subs map[uint64]*sub[T]
	seq  atomic.Uint64
}

// NewEventBus constructs an empty bus.
func NewEventBus[T any]() *EventBus[T] {
	return &EventBus[T]{subs: make(map[uint64]*sub[T])}
}

// Subscribe registers handler to receive events. Returns a Subscription whose
// Cancel method removes the registration.
func (b *EventBus[T]) Subscribe(handler func(T)) Subscription {
	id := b.seq.Add(1)
	s := &sub[T]{
		id:      id,
		ch:      make(chan T, 64),
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

// Publish delivers event to all current subscribers in a non-blocking fashion.
// Subscribers whose channel is full will miss the event (back-pressure is the
// subscriber's responsibility via a larger buffer or faster handler).
func (b *EventBus[T]) Publish(event T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		select {
		case s.ch <- event:
		default:
			// subscriber is slow; skip rather than block the publisher
		}
	}
}

// Close drains and removes all subscribers.
func (b *EventBus[T]) Close() {
	b.mu.Lock()
	subs := b.subs
	b.subs = make(map[uint64]*sub[T])
	b.mu.Unlock()
	for _, s := range subs {
		s.close()
	}
}

// sub is a single subscription.
type sub[T any] struct {
	id      uint64
	ch      chan T
	bus     *EventBus[T]
	handler func(T)
	done    chan struct{}
	once    sync.Once
}

func (s *sub[T]) drain() {
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

func (s *sub[T]) close() {
	s.once.Do(func() {
		close(s.done)
	})
}

// Cancel implements Subscription.
func (s *sub[T]) Cancel() {
	s.bus.mu.Lock()
	delete(s.bus.subs, s.id)
	s.bus.mu.Unlock()
	s.close()
}

// ─── Subscription ─────────────────────────────────────────────────────────────

// Subscription represents an active event subscription.
type Subscription interface {
	// Cancel removes this subscription from its bus. Safe to call multiple times.
	Cancel()
}

// ─── Observable ───────────────────────────────────────────────────────────────

// Observable[T] holds a current value and notifies subscribers whenever it
// changes. It is safe for concurrent access.
type Observable[T comparable] struct {
	mu       sync.RWMutex
	current  T
	bus      *EventBus[ChangeEvent[T]]
}

// ChangeEvent carries the old and new value from an Observable update.
type ChangeEvent[T any] struct {
	Old T
	New T
}

// NewObservable creates an Observable with an initial value.
func NewObservable[T comparable](initial T) *Observable[T] {
	return &Observable[T]{
		current: initial,
		bus:     NewEventBus[ChangeEvent[T]](),
	}
}

// Value returns the current value.
func (o *Observable[T]) Value() T {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.current
}

// Set updates the value. If the new value differs from the current one, a
// ChangeEvent is published to all subscribers.
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

// Subscribe registers a handler to be called on each change.
func (o *Observable[T]) Subscribe(handler func(ChangeEvent[T])) Subscription {
	return o.bus.Subscribe(handler)
}

// Close shuts down the underlying bus.
func (o *Observable[T]) Close() {
	o.bus.Close()
}
