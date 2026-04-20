package reactdag

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// Observer — typed, filterable event subscription
// ---------------------------------------------------------------------------

// ObserveFilter selects which events an Observer receives.
// Return true to accept the event, false to discard it.
type ObserveFilter func(e Event) bool

// ObserveOption configures an Observer.
type ObserveOption func(*observerConfig)

type observerConfig struct {
	filter    ObserveFilter
	bufSize   int
	onOverflow OverflowPolicy
}

// OverflowPolicy controls what happens when the Observer's channel is full.
type OverflowPolicy uint8

const (
	// OverflowDrop silently discards new events when the buffer is full.
	// The subscriber keeps running; some events are lost.
	OverflowDrop OverflowPolicy = iota
	// OverflowBlock makes the publisher wait until the subscriber reads.
	// This back-pressures the entire build if the subscriber is slow.
	OverflowBlock
)

// WithFilter sets a predicate that selects which events this Observer sees.
func WithFilter(f ObserveFilter) ObserveOption {
	return func(c *observerConfig) { c.filter = f }
}

// WithBufferSize sets the channel buffer depth (default 256).
func WithBufferSize(n int) ObserveOption {
	return func(c *observerConfig) { c.bufSize = n }
}

// WithOverflowPolicy sets the back-pressure policy (default OverflowDrop).
func WithOverflowPolicy(p OverflowPolicy) ObserveOption {
	return func(c *observerConfig) { c.onOverflow = p }
}

// ForEventTypes returns a filter that accepts only the listed event types.
func ForEventTypes(types ...EventType) ObserveFilter {
	set := make(map[EventType]bool, len(types))
	for _, t := range types {
		set[t] = true
	}
	return func(e Event) bool { return set[e.Type] }
}

// ForVertices returns a filter that accepts only events for the listed vertex IDs.
func ForVertices(ids ...string) ObserveFilter {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(e Event) bool { return set[e.VertexID] }
}

// ---------------------------------------------------------------------------
// Observer — a typed, buffered event channel
// ---------------------------------------------------------------------------

// Observer is a live event stream vended by Scheduler.Observe().
// Read events from Events() until the channel is closed.
type Observer struct {
	ch     chan Event
	unsub  func()
	once   sync.Once
	cfg    observerConfig
}

// Events returns the channel of incoming events.
// The channel is closed when Unsubscribe() is called.
func (o *Observer) Events() <-chan Event { return o.ch }

// Unsubscribe removes this Observer from the EventBus and closes Events().
// It is safe to call multiple times.
func (o *Observer) Unsubscribe() {
	o.once.Do(func() {
		o.unsub()
		close(o.ch)
	})
}

// Drain reads and discards all remaining events.
// Useful for cleaning up after Unsubscribe() without ranging over the channel manually.
func (o *Observer) Drain() {
	for range o.ch {
	}
}

// ---------------------------------------------------------------------------
// Scheduler.Observe — vend an Observer from the Scheduler's EventBus
// ---------------------------------------------------------------------------

// Observe creates a new Observer subscribed to the Scheduler's EventBus.
// Events matching the (optional) filter are delivered to the Observer's channel.
//
//	obs := sched.Observe(
//	    dag.WithFilter(dag.ForEventTypes(dag.EventStateChanged, dag.EventExecutionEnd)),
//	    dag.WithBufferSize(512),
//	)
//	for e := range obs.Events() {
//	    log.Printf("%s %s", e.Type, e.VertexID)
//	}
func (s *Scheduler) Observe(opts ...ObserveOption) *Observer {
	cfg := observerConfig{bufSize: 256, onOverflow: OverflowDrop}
	for _, o := range opts {
		o(&cfg)
	}

	obs := &Observer{
		ch:  make(chan Event, cfg.bufSize),
		cfg: cfg,
	}

	handler := func(_ context.Context, e Event) {
		if cfg.filter != nil && !cfg.filter(e) {
			return
		}
		switch cfg.onOverflow {
		case OverflowBlock:
			obs.ch <- e
		default:
			select {
			case obs.ch <- e:
			default: // drop
			}
		}
	}

	unsub := s.cfg.eventBus.SubscribeAll(handler)
	obs.unsub = unsub
	return obs
}

// ---------------------------------------------------------------------------
// EventStream — fan-out a single Observer to multiple typed handlers
// ---------------------------------------------------------------------------

// EventStream wraps an Observer and routes events to per-type handlers
// registered after creation. Useful for building reactive pipelines.
type EventStream struct {
	obs      *Observer
	handlers map[EventType][]EventHandler
	mu       sync.RWMutex
}

// NewEventStream creates an EventStream wrapping obs.
// Start the dispatch loop with Run().
func NewEventStream(obs *Observer) *EventStream {
	return &EventStream{
		obs:      obs,
		handlers: make(map[EventType][]EventHandler),
	}
}

// On registers a handler for a specific event type.
// Returns a deregister function.
func (es *EventStream) On(eventType EventType, h EventHandler) (deregister func()) {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.handlers[eventType] = append(es.handlers[eventType], h)
	idx := len(es.handlers[eventType]) - 1
	return func() {
		es.mu.Lock()
		defer es.mu.Unlock()
		s := es.handlers[eventType]
		if idx < len(s) {
			s[idx] = s[len(s)-1]
			es.handlers[eventType] = s[:len(s)-1]
		}
	}
}

// Run dispatches events from the Observer to registered handlers.
// Blocks until the Observer's channel is closed or ctx is cancelled.
func (es *EventStream) Run(ctx context.Context) {
	for {
		select {
		case e, ok := <-es.obs.Events():
			if !ok {
				return
			}
			es.dispatch(ctx, e)
		case <-ctx.Done():
			return
		}
	}
}

func (es *EventStream) dispatch(ctx context.Context, e Event) {
	es.mu.RLock()
	hs := append([]EventHandler(nil), es.handlers[e.Type]...)
	es.mu.RUnlock()
	for _, h := range hs {
		h(ctx, e)
	}
}
