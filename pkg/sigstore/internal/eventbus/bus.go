// Package eventbus defines the EventBus interface and all related types.
//
// Design rationale:
//   - The interface is intentionally narrow: Publish and Subscribe only.
//     Routing, fan-out, and backpressure are implementation details.
//   - Handler is a plain func, not an interface, keeping subscriber code
//     free of boilerplate wrappers.
//   - Returning an error from Publish is a deliberate choice: callers must
//     handle back-pressure explicitly rather than silently dropping events.
package eventbus

import (
	"context"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
)

// Handler processes a single event envelope. Implementations must be
// idempotent — the bus may redeliver on crash recovery.
// A non-nil error signals the bus to nack/retry per its ResiliencePolicy.
type Handler func(ctx context.Context, env domain.Envelope) error

// Subscription is a handle returned by Subscribe. Cancel it to stop
// receiving events and release the underlying channel/goroutine.
type Subscription interface {
	// Cancel unsubscribes and blocks until the handler goroutine exits.
	Cancel()
	// Topic returns the event type this subscription listens on.
	Topic() domain.EventType
}

// EventBus is the central pub/sub contract.
//
// Interface Segregation (ISP): callers that only publish import Publisher;
// callers that only consume import Subscriber. EventBus composes both for
// wiring code that needs the full picture.
type EventBus interface {
	Publisher
	Subscriber
	// Close drains in-flight events and releases resources.
	// Must be idempotent and safe to call from signal handlers.
	Close() error
}

// Publisher is the write side of the bus.
type Publisher interface {
	// Publish routes env to all matching subscribers.
	// Returns ErrBusFull when the internal buffer is at capacity (backpressure).
	Publish(ctx context.Context, env domain.Envelope) error
}

// Subscriber is the read side of the bus.
type Subscriber interface {
	// Subscribe registers handler for the given topic.
	// Multiple handlers per topic are supported (fan-out).
	// Returns ErrBusClosed if called after Close().
	Subscribe(topic domain.EventType, handler Handler) (Subscription, error)
}

// --- sentinel errors --------------------------------------------------------

// ErrBusFull is returned by Publish when the channel buffer is exhausted.
// Callers should apply backpressure to their upstream rather than retrying
// blindly.
type ErrBusFull struct{ Topic domain.EventType }

func (e ErrBusFull) Error() string {
	return "event bus full for topic: " + string(e.Topic)
}

// ErrBusClosed is returned on operations after Close() has been called.
type ErrBusClosed struct{}

func (e ErrBusClosed) Error() string { return "event bus is closed" }
