// Package signal provides a non-blocking event bus for reactive notifications
// during solve sessions. It enables subscribers to observe vertex state
// transitions without coupling the scheduler to consumers, matching
// BuildKit's progress/event reporting philosophy.
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
	// CacheMiss is emitted when no cached result exists.
	CacheMiss
	// VertexCompleted is emitted when a vertex finishes successfully.
	VertexCompleted
	// VertexFailed is emitted when a vertex execution fails.
	VertexFailed
	// VertexCanceled is emitted when a vertex is cancelled via context.
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

// Event represents a single solver lifecycle event.
type Event struct {
	Type      EventType
	Vertex    digest.Digest
	Name      string
	Timestamp time.Time
	Error     error
	ResultID  string
}

// ─── Bus ──────────────────────────────────────────────────────────────────────

// Bus is a non-blocking, fan-out event bus. Publishers are never blocked: if a
// subscriber's channel is full the event is dropped for that subscriber and the
// drop counter is incremented. This prevents slow consumers from degrading
// build throughput.
type Bus struct {
	mu          sync.RWMutex
	subscribers []*Subscription
	closed      atomic.Bool
}

// NewBus creates a new event bus.
func NewBus() *Bus { return &Bus{} }

// Publish sends evt to all active subscribers that match their filter.
// Non-blocking: events are dropped (not queued) for slow subscribers.
func (b *Bus) Publish(evt Event) {
	if b.closed.Load() {
		return
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	b.mu.RLock()
	subs := b.subscribers // snapshot; safe because we never mutate after assignment
	b.mu.RUnlock()
	for _, sub := range subs {
		if sub.closed.Load() {
			continue
		}
		if sub.filter != nil && !sub.filter(evt) {
			continue
		}
		select {
		case sub.ch <- evt:
		default:
			sub.dropped.Add(1)
		}
	}
}

// Subscribe creates a new subscription. bufSize is the event channel buffer;
// filter (if non-nil) restricts which events are delivered.
// The caller must consume from sub.Events() promptly or events will be dropped.
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

// Close closes the bus and all subscriptions. Idempotent.
func (b *Bus) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	for _, sub := range b.subscribers {
		sub.Close()
	}
	b.subscribers = nil
	b.mu.Unlock()
}

// ─── Subscription ─────────────────────────────────────────────────────────────

// Subscription receives events from a Bus.
type Subscription struct {
	ch      chan Event
	filter  func(Event) bool
	closed  atomic.Bool
	dropped atomic.Int64
}

// Events returns the read-only event channel.
func (s *Subscription) Events() <-chan Event { return s.ch }

// Dropped returns the number of events dropped due to a full buffer.
func (s *Subscription) Dropped() int64 { return s.dropped.Load() }

// Close closes this subscription. Idempotent.
func (s *Subscription) Close() {
	if s.closed.CompareAndSwap(false, true) {
		close(s.ch)
	}
}

// ─── Filter constructors ──────────────────────────────────────────────────────

// ForVertex returns a filter that only passes events for the given vertex.
func ForVertex(dgst digest.Digest) func(Event) bool {
	return func(evt Event) bool { return evt.Vertex == dgst }
}

// ForTypes returns a filter that only passes specific event types.
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

// ForVertexTypes composes ForVertex and ForTypes into a single filter.
// Useful for watching specific lifecycle events of one vertex.
func ForVertexTypes(dgst digest.Digest, types ...EventType) func(Event) bool {
	byVertex := ForVertex(dgst)
	byType := ForTypes(types...)
	return func(evt Event) bool {
		return byVertex(evt) && byType(evt)
	}
}

// And combines two filters with logical AND.
func And(a, b func(Event) bool) func(Event) bool {
	return func(evt Event) bool { return a(evt) && b(evt) }
}

// Or combines two filters with logical OR.
func Or(a, b func(Event) bool) func(Event) bool {
	return func(evt Event) bool { return a(evt) || b(evt) }
}

// Not negates a filter.
func Not(f func(Event) bool) func(Event) bool {
	return func(evt Event) bool { return !f(evt) }
}
