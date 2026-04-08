package event

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// subscription is an internal record of a registered handler.
type subscription struct {
	id        string
	topic     Topic
	handler   Handler
	predicate Predicate // nil → accept all
}

// BusOption configures a Bus at construction time.
type BusOption func(*Bus)

// WithBusLogger attaches a structured logger to the bus.
func WithBusLogger(l *zap.Logger) BusOption {
	return func(b *Bus) { b.logger = l }
}

// Bus is a topic-based, in-process event bus.
//
// Design invariants:
//   - The subscriber map is only mutated under a write lock.
//   - Handlers are invoked outside the lock (snapshot-then-deliver).
//   - Async delivery uses a dedicated goroutine; the main goroutine never blocks.
//   - Closing the bus is idempotent; subsequent publishes are silently discarded.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[Topic][]*subscription

	// Async delivery channel. nil when asyncBufferSize == 0.
	async chan Event
	wg    sync.WaitGroup

	closed atomic.Bool
	nextID atomic.Uint64
	logger *zap.Logger
}

// NewBus constructs a Bus.
//
// asyncBufferSize > 0 enables non-blocking asynchronous delivery via a
// buffered channel processed by a single background goroutine.
// asyncBufferSize == 0 means PublishAsync falls back to synchronous delivery.
func NewBus(asyncBufferSize int, opts ...BusOption) *Bus {
	b := &Bus{
		subscribers: make(map[Topic][]*subscription),
		logger:      zap.NewNop(),
	}
	for _, o := range opts {
		o(b)
	}
	if asyncBufferSize > 0 {
		b.async = make(chan Event, asyncBufferSize)
		b.wg.Add(1)
		go b.drain()
	}
	return b
}

// Subscribe registers a handler for a topic. The returned string is an opaque
// subscription ID; pass it to Unsubscribe to deregister.
//
// An optional Predicate narrows which events invoke the handler. When multiple
// predicates are given, all must return true (logical AND).
func (b *Bus) Subscribe(topic Topic, h Handler, predicates ...Predicate) string {
	id := fmt.Sprintf("sub-%d", b.nextID.Add(1))

	var pred Predicate
	switch len(predicates) {
	case 0:
		// nil → accept all; checked in deliver
	case 1:
		pred = predicates[0]
	default:
		pred = allPredicates(predicates)
	}

	sub := &subscription{id: id, topic: topic, handler: h, predicate: pred}

	b.mu.Lock()
	b.subscribers[topic] = append(b.subscribers[topic], sub)
	b.mu.Unlock()

	return id
}

// Unsubscribe removes a subscription by ID. It is safe to call concurrently
// and is a no-op for unknown IDs.
func (b *Bus) Unsubscribe(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for topic, subs := range b.subscribers {
		out := subs[:0]
		for _, s := range subs {
			if s.id != id {
				out = append(out, s)
			}
		}
		b.subscribers[topic] = out
	}
}

// Publish delivers an event synchronously. The call blocks until every
// matching handler has returned.
//
// Calling Publish on a nil Bus is a safe no-op; this allows components to be
// constructed with a nil bus before the engine creates the real one.
func (b *Bus) Publish(ctx context.Context, topic Topic, payload any, correlationID string) {
	if b == nil || b.closed.Load() {
		return
	}
	b.deliver(b.newEvent(ctx, topic, payload, correlationID))
}

// PublishAsync enqueues an event for delivery on the background goroutine.
// Returns false (and logs a warning) if the bus is closed or the queue is full.
// Falls back to synchronous delivery when the bus was created without a queue.
//
// Calling PublishAsync on a nil Bus is a safe no-op and returns false.
func (b *Bus) PublishAsync(ctx context.Context, topic Topic, payload any, correlationID string) bool {
	if b == nil || b.closed.Load() {
		return false
	}
	e := b.newEvent(ctx, topic, payload, correlationID)

	if b.async == nil {
		// No async channel configured; deliver synchronously.
		b.deliver(e)
		return true
	}

	select {
	case b.async <- e:
		return true
	default:
		b.logger.Warn("event bus async queue full; dropping event",
			zap.String("topic", string(topic)),
			zap.String("correlation_id", correlationID),
		)
		return false
	}
}

// Close drains the async queue, waits for all in-flight handlers to finish,
// and marks the bus as closed. Subsequent publishes are silently dropped.
// Safe to call more than once.
func (b *Bus) Close() {
	if b.closed.Swap(true) {
		return // already closed
	}
	if b.async != nil {
		close(b.async)
		b.wg.Wait()
	}
}

// ── internal ─────────────────────────────────────────────────────────────────

func (b *Bus) newEvent(ctx context.Context, topic Topic, payload any, correlationID string) Event {
	return Event{
		ID:            fmt.Sprintf("evt-%d", b.nextID.Add(1)),
		CorrelationID: correlationID,
		Topic:         topic,
		Timestamp:     time.Now().UTC(),
		Payload:       payload,
		ctx:           ctx,
	}
}

// drain is the single goroutine consuming the async channel.
func (b *Bus) drain() {
	defer b.wg.Done()
	for e := range b.async {
		b.deliver(e)
	}
}

// deliver dispatches e to all matching subscribers.
// It takes a snapshot of the subscriber slice under a read-lock, then invokes
// handlers outside the lock to avoid holding it during potentially slow work.
func (b *Bus) deliver(e Event) {
	b.mu.RLock()
	subs := b.subscribers[e.Topic]
	snapshot := make([]*subscription, len(subs))
	copy(snapshot, subs)
	b.mu.RUnlock()

	for _, s := range snapshot {
		if s.predicate != nil && !s.predicate(e) {
			continue
		}
		if err := s.handler(e); err != nil {
			b.logger.Error("event handler error",
				zap.String("subscription_id", s.id),
				zap.String("topic", string(e.Topic)),
				zap.String("correlation_id", e.CorrelationID),
				zap.Error(err),
			)
		}
	}
}

// allPredicates composes multiple predicates with logical AND.
func allPredicates(ps []Predicate) Predicate {
	return func(e Event) bool {
		for _, p := range ps {
			if !p(e) {
				return false
			}
		}
		return true
	}
}
