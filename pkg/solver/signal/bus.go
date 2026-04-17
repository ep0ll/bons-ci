// Package signal provides an event bus for reactive notifications during
// solve sessions. It enables subscribers to observe vertex state transitions
// without coupling the scheduler to consumers.
package signal

import (
	"sync"
	"sync/atomic"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// EventType classifies solver events.
type EventType int

const (
	// VertexQueued is emitted when a vertex is added to the scheduler queue.
	VertexQueued EventType = iota + 1

	// VertexStarted is emitted when a vertex begins execution.
	VertexStarted

	// CacheHit is emitted when a vertex's result is found in cache.
	CacheHit

	// CacheMiss is emitted when no cached result exists for a vertex.
	CacheMiss

	// VertexCompleted is emitted when a vertex finishes successfully.
	VertexCompleted

	// VertexFailed is emitted when a vertex execution fails.
	VertexFailed

	// VertexCanceled is emitted when a vertex is canceled via context.
	VertexCanceled
)

// String returns the event type name.
func (t EventType) String() string {
	switch t {
	case VertexQueued:
		return "queued"
	case VertexStarted:
		return "started"
	case CacheHit:
		return "cache-hit"
	case CacheMiss:
		return "cache-miss"
	case VertexCompleted:
		return "completed"
	case VertexFailed:
		return "failed"
	case VertexCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// Event represents a solver event.
type Event struct {
	Type      EventType
	Vertex    digest.Digest
	Name      string
	Timestamp time.Time
	Error     error
	ResultID  string
}

// Bus is a non-blocking event bus. Publishers never block; slow
// subscribers drop events rather than causing back-pressure.
type Bus struct {
	mu          sync.RWMutex
	subscribers []*Subscription
	closed      atomic.Bool
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{}
}

// Publish sends an event to all active subscribers. Non-blocking: if a
// subscriber's channel is full, the event is dropped for that subscriber.
func (b *Bus) Publish(evt Event) {
	if b.closed.Load() {
		return
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subscribers {
		if sub.closed.Load() {
			continue
		}
		if sub.filter != nil && !sub.filter(evt) {
			continue
		}
		select {
		case sub.ch <- evt:
		default:
			// Drop: subscriber is slow.
			sub.dropped.Add(1)
		}
	}
}

// Subscribe creates a new subscription with an optional filter.
// The channel buffer size controls how many events can be buffered
// before dropping.
func (b *Bus) Subscribe(bufSize int, filter func(Event) bool) *Subscription {
	if bufSize < 1 {
		bufSize = 64
	}
	sub := &Subscription{
		ch:     make(chan Event, bufSize),
		filter: filter,
	}
	b.mu.Lock()
	b.subscribers = append(b.subscribers, sub)
	b.mu.Unlock()
	return sub
}

// Close closes the bus and all subscriptions.
func (b *Bus) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subscribers {
		sub.Close()
	}
	b.subscribers = nil
}

// Subscription receives events from the bus.
type Subscription struct {
	ch      chan Event
	filter  func(Event) bool
	closed  atomic.Bool
	dropped atomic.Int64
}

// Events returns the channel of events for this subscription.
func (s *Subscription) Events() <-chan Event {
	return s.ch
}

// Dropped returns the number of events dropped due to a full buffer.
func (s *Subscription) Dropped() int64 {
	return s.dropped.Load()
}

// Close closes this subscription's channel.
func (s *Subscription) Close() {
	if s.closed.CompareAndSwap(false, true) {
		close(s.ch)
	}
}

// ForVertex returns a filter that matches events for a specific vertex.
func ForVertex(dgst digest.Digest) func(Event) bool {
	return func(evt Event) bool {
		return evt.Vertex == dgst
	}
}

// ForTypes returns a filter that matches specific event types.
func ForTypes(types ...EventType) func(Event) bool {
	set := make(map[EventType]struct{}, len(types))
	for _, t := range types {
		set[t] = struct{}{}
	}
	return func(evt Event) bool {
		_, ok := set[evt.Type]
		return ok
	}
}
