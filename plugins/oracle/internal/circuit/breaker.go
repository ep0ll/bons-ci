// Package circuit provides a circuit-breaker wrapper for OCI SDK calls.
// It prevents cascading failures when the OCI control plane is degraded —
// critical during the 120-second migration window where every API call counts.
//
// States:
//
//	Closed   — normal operation, all calls pass through.
//	Open     — OCI is unhealthy; calls fail fast without hitting the API.
//	Half-open — one probe allowed; if it succeeds, close; if not, stay open.
package circuit

import (
	"context"
	"fmt"
	"time"

	"github.com/sony/gobreaker"
	"go.uber.org/zap"
)

// Settings controls breaker sensitivity.
type Settings struct {
	// MaxRequests in half-open state before deciding whether to close.
	MaxRequests uint32
	// Interval is the rolling window for failure counting.
	Interval time.Duration
	// Timeout is how long the breaker stays open before attempting half-open.
	Timeout time.Duration
	// FailureRatio is the fraction of failures (0-1) that triggers open state.
	FailureRatio float64
	// MinRequests before failure ratio is evaluated.
	MinRequests uint32
}

// DefaultSettings returns production-tuned defaults.
func DefaultSettings() Settings {
	return Settings{
		MaxRequests:  3,
		Interval:     30 * time.Second,
		Timeout:      15 * time.Second,
		FailureRatio: 0.6,
		MinRequests:  5,
	}
}

// Breaker wraps gobreaker with zap logging and context propagation.
type Breaker struct {
	cb  *gobreaker.CircuitBreaker
	log *zap.Logger
}

// New constructs a named Breaker.
func New(name string, s Settings, log *zap.Logger) *Breaker {
	b := &Breaker{log: log}
	b.cb = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: s.MaxRequests,
		Interval:    s.Interval,
		Timeout:     s.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < uint32(s.MinRequests) {
				return false
			}
			ratio := float64(counts.TotalFailures) / float64(counts.Requests)
			return ratio >= s.FailureRatio
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			log.Warn("circuit breaker state change",
				zap.String("breaker", name),
				zap.String("from", from.String()),
				zap.String("to", to.String()),
			)
		},
	})
	return b
}

// Execute runs fn inside the circuit breaker.
// Returns ErrCircuitOpen if the breaker is open.
func (b *Breaker) Execute(ctx context.Context, fn func() error) error {
	_, err := b.cb.Execute(func() (interface{}, error) {
		// Respect context cancellation inside the breaker.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return nil, fn()
	})
	if err == gobreaker.ErrOpenState {
		return fmt.Errorf("circuit open for %s — OCI API unavailable: %w", b.cb.Name(), err)
	}
	return err
}

// ExecuteTyped is a generic helper that returns a typed value.
func ExecuteTyped[T any](ctx context.Context, b *Breaker, fn func() (T, error)) (T, error) {
	var zero T
	result, err := b.cb.Execute(func() (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		v, err := fn()
		return v, err
	})
	if err != nil {
		return zero, err
	}
	if result == nil {
		return zero, nil
	}
	return result.(T), nil
}

// State returns the current breaker state as a string.
func (b *Breaker) State() string {
	return b.cb.State().String()
}
