package reactdag

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// EventBus
// ---------------------------------------------------------------------------

type subscription struct {
	id      uint64
	handler EventHandler
}

// EventBus is a thread-safe publish-subscribe hub for DAG lifecycle events.
// Handlers are called synchronously in the publishing goroutine; for
// long-running handlers, dispatch to a separate goroutine inside the handler.
type EventBus struct {
	mu     sync.RWMutex
	subs   map[EventType][]subscription
	nextID uint64
}

// NewEventBus constructs a ready-to-use EventBus.
func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[EventType][]subscription)}
}

// Subscribe registers a handler for the given event type.
// The returned function deregisters the handler when called.
func (b *EventBus) Subscribe(eventType EventType, handler EventHandler) (unsubscribe func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	b.subs[eventType] = append(b.subs[eventType], subscription{id: id, handler: handler})
	return func() { b.unsubscribe(eventType, id) }
}

// SubscribeAll registers a handler for every event type.
func (b *EventBus) SubscribeAll(handler EventHandler) (unsubscribe func()) {
	// Sentinel key used internally for wildcard subscriptions.
	const wildcard EventType = "*"
	return b.Subscribe(wildcard, handler)
}

// Publish emits an event to all matching (and wildcard) subscribers.
func (b *EventBus) Publish(ctx context.Context, e Event) {
	b.mu.RLock()
	handlers := b.collect(e.Type)
	b.mu.RUnlock()
	for _, h := range handlers {
		h(ctx, e)
	}
}

// collect gathers handlers for the given type plus wildcard subscribers.
// Caller must hold at least a read lock.
func (b *EventBus) collect(t EventType) []EventHandler {
	var out []EventHandler
	for _, sub := range b.subs[t] {
		out = append(out, sub.handler)
	}
	for _, sub := range b.subs["*"] {
		out = append(out, sub.handler)
	}
	return out
}

// unsubscribe removes a subscription by ID.
func (b *EventBus) unsubscribe(eventType EventType, id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subs[eventType]
	for i, s := range subs {
		if s.id == id {
			b.subs[eventType] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Typed publish helpers
// ---------------------------------------------------------------------------

// PublishStateChanged emits EventStateChanged with from/to state data.
func (b *EventBus) PublishStateChanged(ctx context.Context, v *Vertex, from, to State) {
	e := newEvent(EventStateChanged, v.ID())
	e.Payload["from"] = from.String()
	e.Payload["to"] = to.String()
	b.Publish(ctx, e)
}

// PublishCacheHit emits EventCacheHit with cache tier information.
func (b *EventBus) PublishCacheHit(ctx context.Context, v *Vertex, tier string) {
	e := newEvent(EventCacheHit, v.ID())
	e.Payload["tier"] = tier
	b.Publish(ctx, e)
}

// PublishCacheMiss emits EventCacheMiss.
func (b *EventBus) PublishCacheMiss(ctx context.Context, v *Vertex) {
	b.Publish(ctx, newEvent(EventCacheMiss, v.ID()))
}

// PublishExecutionStart emits EventExecutionStart.
func (b *EventBus) PublishExecutionStart(ctx context.Context, v *Vertex) {
	b.Publish(ctx, newEvent(EventExecutionStart, v.ID()))
}

// PublishExecutionEnd emits EventExecutionEnd with duration and error data.
func (b *EventBus) PublishExecutionEnd(ctx context.Context, v *Vertex) {
	e := newEvent(EventExecutionEnd, v.ID())
	m := v.Metrics()
	e.Payload["duration_ms"] = m.Duration().Milliseconds()
	if err := v.Err(); err != nil {
		e.Payload["error"] = err.Error()
	}
	b.Publish(ctx, e)
}

// PublishInvalidated emits EventInvalidated.
func (b *EventBus) PublishInvalidated(ctx context.Context, v *Vertex, reason string) {
	e := newEvent(EventInvalidated, v.ID())
	e.Payload["reason"] = reason
	b.Publish(ctx, e)
}

// PublishBuildStart emits EventBuildStart with the target vertex ID.
func (b *EventBus) PublishBuildStart(ctx context.Context, targetID string) {
	e := newEvent(EventBuildStart, targetID)
	b.Publish(ctx, e)
}

// PublishBuildEnd emits EventBuildEnd with aggregate metrics.
func (b *EventBus) PublishBuildEnd(ctx context.Context, targetID string, m *BuildMetrics) {
	e := newEvent(EventBuildEnd, targetID)
	e.Payload["executed"] = m.Executed
	e.Payload["fast_cache_hits"] = m.FastCacheHits
	e.Payload["slow_cache_hits"] = m.SlowCacheHits
	e.Payload["failed"] = m.Failed
	e.Payload["duration_ms"] = m.TotalDuration.Milliseconds()
	b.Publish(ctx, e)
}
