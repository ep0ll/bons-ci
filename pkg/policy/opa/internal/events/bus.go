// Package events provides a typed, OTEL-instrumented publish-subscribe bus.
//
// Design invariants (verified by tests):
//   - Delivery is synchronous in the publisher's goroutine by default.
//   - AsyncHandler wraps any handler for fire-and-forget delivery.
//   - Subscription.Cancel is idempotent and immediately stops delivery.
//   - The Bus itself never panics; handler panics are recovered.
//   - Wildcard subscriptions (Kind=="") receive every event.
//   - All operations are goroutine-safe.
package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
)

// ─── Kind ────────────────────────────────────────────────────────────────────

// Kind is an opaque event category string.
// Packages declare their own Kind constants; the bus treats them as opaque.
type Kind string

// ─── Event / RawEvent ─────────────────────────────────────────────────────────

// RawEvent is the wire format transported by the bus.
type RawEvent struct {
	Kind      Kind
	Payload   interface{}
	Meta      map[string]string
	Timestamp time.Time
}

func (r RawEvent) String() string {
	return fmt.Sprintf("Event{kind=%q ts=%s}", r.Kind, r.Timestamp.Format(time.RFC3339Nano))
}

// Event is a typed envelope. Use PublishTyped to emit; use On to subscribe.
type Event[P any] struct {
	Kind    Kind
	Payload P
	Meta    map[string]string
}

// ─── Handler ─────────────────────────────────────────────────────────────────

// Handler processes an event. A non-nil return value is forwarded to the Bus
// error handler; it does NOT stop delivery to other subscribers.
type Handler func(ctx context.Context, e RawEvent) error

// ─── Subscription ─────────────────────────────────────────────────────────────

// Subscription represents an active handler registration.
// Cancel is idempotent.
type Subscription struct {
	once   sync.Once
	cancel func()
}

// Cancel unregisters the handler. Safe to call multiple times and from
// concurrent goroutines.
func (s *Subscription) Cancel() {
	s.once.Do(s.cancel)
}

// ─── Bus ─────────────────────────────────────────────────────────────────────

// Bus is the publish-subscribe hub. It is goroutine-safe and instrumented.
type Bus struct {
	mu    sync.RWMutex
	subs  map[Kind]map[uint64]Handler // specific-kind subscribers
	wild  map[uint64]Handler          // wildcard (Kind=="") subscribers
	seq   atomic.Uint64
	onErr func(error, RawEvent)

	// metrics
	published metric.Int64Counter
	delivered metric.Int64Counter
	dropped   metric.Int64Counter
	handErr   metric.Int64Counter
}

// NewBus creates a Bus. onErr is called when any handler returns a non-nil error.
// Pass nil to silently discard handler errors.
func NewBus(onErr func(error, RawEvent)) (*Bus, error) {
	m := polOtel.Meter("events")
	prefix := polOtel.Namespace + ".events"

	pub, err := m.Int64Counter(prefix+".published_total",
		metric.WithDescription("Events published to bus"))
	if err != nil {
		return nil, fmt.Errorf("events: metric published: %w", err)
	}
	del, err := m.Int64Counter(prefix+".delivered_total",
		metric.WithDescription("Handler invocations"))
	if err != nil {
		return nil, fmt.Errorf("events: metric delivered: %w", err)
	}
	drop, err := m.Int64Counter(prefix+".dropped_total",
		metric.WithDescription("Events dropped due to full async buffer"))
	if err != nil {
		return nil, fmt.Errorf("events: metric dropped: %w", err)
	}
	herr, err := m.Int64Counter(prefix+".handler_errors_total",
		metric.WithDescription("Handler invocations that returned an error"))
	if err != nil {
		return nil, fmt.Errorf("events: metric handler_errors: %w", err)
	}

	if onErr == nil {
		onErr = func(error, RawEvent) {}
	}

	return &Bus{
		subs:      make(map[Kind]map[uint64]Handler),
		wild:      make(map[uint64]Handler),
		onErr:     onErr,
		published: pub,
		delivered: del,
		dropped:   drop,
		handErr:   herr,
	}, nil
}

// Subscribe registers h to receive events of kind. Use kind="" for all events.
// Returns a Subscription whose Cancel method deregisters h.
func (b *Bus) Subscribe(kind Kind, h Handler) *Subscription {
	id := b.seq.Add(1)

	b.mu.Lock()
	if kind == "" {
		b.wild[id] = h
	} else {
		if b.subs[kind] == nil {
			b.subs[kind] = make(map[uint64]Handler)
		}
		b.subs[kind][id] = h
	}
	b.mu.Unlock()

	return &Subscription{cancel: func() {
		b.mu.Lock()
		if kind == "" {
			delete(b.wild, id)
		} else {
			delete(b.subs[kind], id)
		}
		b.mu.Unlock()
	}}
}

// Publish delivers e to all matching subscribers synchronously.
// Panics inside handlers are recovered and forwarded to the error handler.
func (b *Bus) Publish(ctx context.Context, e RawEvent) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	attrs := metric.WithAttributes(
		polOtel.AttrEventKind.String(string(e.Kind)),
	)
	b.published.Add(ctx, 1, attrs)

	b.mu.RLock()
	specific := cloneMap(b.subs[e.Kind])
	wild := cloneMap(b.wild)
	b.mu.RUnlock()

	deliver := func(h Handler) {
		b.delivered.Add(ctx, 1, attrs)
		var retErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					retErr = fmt.Errorf("events: handler panic: %v", r)
				}
			}()
			retErr = h(ctx, e)
		}()
		if retErr != nil {
			b.handErr.Add(ctx, 1, attrs)
			b.onErr(retErr, e)
		}
	}

	for _, h := range specific {
		deliver(h)
	}
	for _, h := range wild {
		deliver(h)
	}
}

// PublishTyped wraps a typed Event into a RawEvent and publishes it.
func PublishTyped[P any](ctx context.Context, b *Bus, e Event[P]) {
	b.Publish(ctx, RawEvent{
		Kind:    e.Kind,
		Payload: e.Payload,
		Meta:    e.Meta,
	})
}

// ─── Typed subscription helper ────────────────────────────────────────────────

// On registers a strongly-typed handler for events whose payload is type P.
// If the payload cannot be type-asserted to P the event is silently skipped.
func On[P any](b *Bus, k Kind, fn func(ctx context.Context, payload P) error) *Subscription {
	return b.Subscribe(k, func(ctx context.Context, e RawEvent) error {
		p, ok := e.Payload.(P)
		if !ok {
			return nil
		}
		return fn(ctx, p)
	})
}

// ─── Handler combinators ──────────────────────────────────────────────────────

// AsyncHandler wraps h so delivery happens in a new goroutine.
// The bus drops the event (and increments dropped metric) when bufSize is
// exhausted; set bufSize=0 for unbounded (use with caution in production).
func (b *Bus) AsyncHandler(h Handler, bufSize int) Handler {
	if bufSize == 0 {
		return func(ctx context.Context, e RawEvent) error {
			go func() { _ = h(ctx, e) }()
			return nil
		}
	}
	ch := make(chan struct {
		ctx context.Context
		e   RawEvent
	}, bufSize)
	go func() {
		for item := range ch {
			_ = h(item.ctx, item.e)
		}
	}()
	return func(ctx context.Context, e RawEvent) error {
		select {
		case ch <- struct {
			ctx context.Context
			e   RawEvent
		}{ctx, e}:
		default:
			b.dropped.Add(ctx, 1, metric.WithAttributes(
				attribute.String("kind", string(e.Kind)),
			))
		}
		return nil
	}
}

// Pipeline chains handlers in order. Stops on the first non-nil error.
func Pipeline(handlers ...Handler) Handler {
	return func(ctx context.Context, e RawEvent) error {
		for _, h := range handlers {
			if err := h(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}
}

// Filter wraps h so it only fires when pred(e) is true.
func Filter(pred func(RawEvent) bool, h Handler) Handler {
	return func(ctx context.Context, e RawEvent) error {
		if !pred(e) {
			return nil
		}
		return h(ctx, e)
	}
}

// Retry wraps h, retrying up to maxAttempts times with constant delay on error.
func Retry(maxAttempts int, delay time.Duration, h Handler) Handler {
	return func(ctx context.Context, e RawEvent) error {
		var lastErr error
		for i := 0; i < maxAttempts; i++ {
			if err := h(ctx, e); err == nil {
				return nil
			} else {
				lastErr = err
			}
			if i < maxAttempts-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
			}
		}
		return fmt.Errorf("events: retry exhausted after %d attempts: %w", maxAttempts, lastErr)
	}
}

// ─── Channel bridge ───────────────────────────────────────────────────────────

// Chan returns a buffered channel that receives events of kind and a cancel func.
// When ctx is cancelled or cancel() is called, the channel is drained and closed.
func Chan(ctx context.Context, b *Bus, kind Kind, bufSize int) (<-chan RawEvent, func()) {
	ch := make(chan RawEvent, bufSize)
	sub := b.Subscribe(kind, func(_ context.Context, e RawEvent) error {
		select {
		case ch <- e:
		case <-ctx.Done():
		}
		return nil
	})
	cancel := func() {
		sub.Cancel()
		for {
			select {
			case <-ch:
			default:
				close(ch)
				return
			}
		}
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ch, cancel
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func cloneMap(m map[uint64]Handler) []Handler {
	if len(m) == 0 {
		return nil
	}
	out := make([]Handler, 0, len(m))
	for _, h := range m {
		out = append(out, h)
	}
	return out
}
