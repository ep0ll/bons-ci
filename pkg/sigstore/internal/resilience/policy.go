// Package resilience provides the ResiliencePolicy interface and concrete
// implementations: exponential-jitter retry and a token-bucket circuit breaker.
//
// Architectural decision: retry and circuit-breaking are orthogonal concerns
// that compose rather than inherit. The service layer injects a ResiliencePolicy
// and calls Execute — it never sees retry counters or circuit state.
//
// Composition pattern:
//
//	CircuitBreaker.Execute(ctx, func() error {
//	    return RetryPolicy.Execute(ctx, actualWork)
//	})
package resilience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/observability"
)

// ResiliencePolicy wraps an operation with resilience semantics.
// Implementations decide whether/how to retry, circuit-break, or timeout.
type ResiliencePolicy interface {
	// Execute runs fn, applying the policy's retry/circuit-break logic.
	// fn receives the attempt-local context; it must respect cancellation.
	Execute(ctx context.Context, fn func(ctx context.Context) error) error
}

// IsRetryable is the contract callers use to classify errors.
// The retry policy calls this before each retry decision.
// Non-retryable errors are propagated immediately without further attempts.
type IsRetryable func(err error) bool

// DefaultIsRetryable treats all errors as retryable except context cancellation/deadline.
func DefaultIsRetryable(err error) bool {
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// --- Retry Policy ───────────────────────────────────────────────────────────

// RetryConfig holds all parameters for the exponential-jitter retry policy.
type RetryConfig struct {
	MaxAttempts int           // total attempts including the first (default: 3)
	InitialWait time.Duration // wait before second attempt (default: 100ms)
	MaxWait     time.Duration // cap on wait (default: 30s)
	Multiplier  float64       // backoff multiplier (default: 2.0)
	JitterFrac  float64       // jitter as fraction of wait (default: 0.3)
	IsRetryable IsRetryable   // nil → DefaultIsRetryable

	Logger  *slog.Logger
	Metrics *observability.Metrics
}

func (c *RetryConfig) withDefaults() RetryConfig {
	out := *c
	if out.MaxAttempts == 0 {
		out.MaxAttempts = 3
	}
	if out.InitialWait == 0 {
		out.InitialWait = 100 * time.Millisecond
	}
	if out.MaxWait == 0 {
		out.MaxWait = 30 * time.Second
	}
	if out.Multiplier == 0 {
		out.Multiplier = 2.0
	}
	if out.JitterFrac == 0 {
		out.JitterFrac = 0.3
	}
	if out.IsRetryable == nil {
		out.IsRetryable = DefaultIsRetryable
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return out
}

// RetryPolicy implements ResiliencePolicy with full-jitter exponential backoff.
//
// Full-jitter formula (AWS best-practice):
//
//	sleep = random(0, min(MaxWait, InitialWait * Multiplier^attempt))
//
// This prevents thundering herds when many callers retry simultaneously.
type RetryPolicy struct {
	cfg RetryConfig
	rng *rand.Rand
	mu  sync.Mutex // guards rng
}

// NewRetryPolicy returns a RetryPolicy with the given config.
func NewRetryPolicy(cfg RetryConfig) *RetryPolicy {
	c := cfg.withDefaults()
	//nolint:gosec // not security-sensitive; seed only affects jitter spread
	return &RetryPolicy{cfg: c, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// Execute runs fn with retry logic. The returned error is the last error seen,
// wrapped with attempt metadata for observability.
func (p *RetryPolicy) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	var lastErr error

	for attempt := 1; attempt <= p.cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return fmt.Errorf("retry aborted (context cancelled): %w", ctx.Err())
		}

		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}

		if !p.cfg.IsRetryable(lastErr) {
			return fmt.Errorf("non-retryable error on attempt %d: %w", attempt, lastErr)
		}

		if attempt == p.cfg.MaxAttempts {
			break
		}

		wait := p.computeWait(attempt)
		p.cfg.Logger.Warn("attempt failed, retrying",
			"attempt", attempt,
			"max_attempts", p.cfg.MaxAttempts,
			"wait_ms", wait.Milliseconds(),
			"error", lastErr,
		)

		if p.cfg.Metrics != nil {
			p.cfg.Metrics.RetryCount.WithLabelValues("retry").Inc()
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("retry wait interrupted: %w", ctx.Err())
		case <-time.After(wait):
		}
	}

	if p.cfg.Metrics != nil {
		p.cfg.Metrics.RetryCount.WithLabelValues("exhausted").Inc()
	}
	return fmt.Errorf("exhausted %d attempts: %w", p.cfg.MaxAttempts, lastErr)
}

func (p *RetryPolicy) computeWait(attempt int) time.Duration {
	base := float64(p.cfg.InitialWait) * math.Pow(p.cfg.Multiplier, float64(attempt-1))
	if base > float64(p.cfg.MaxWait) {
		base = float64(p.cfg.MaxWait)
	}
	p.mu.Lock()
	jitter := p.rng.Float64() * p.cfg.JitterFrac * base
	p.mu.Unlock()
	return time.Duration(base + jitter)
}

// --- Circuit Breaker ────────────────────────────────────────────────────────

// CircuitState represents the circuit breaker FSM state.
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // normal; calls pass through
	CircuitOpen                         // tripped; calls rejected immediately
	CircuitHalfOpen                     // probe; one call allowed through
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig holds tunables for the circuit breaker.
type CircuitBreakerConfig struct {
	// FailureThreshold is consecutive failures that trip the breaker.
	FailureThreshold int

	// SuccessThreshold is consecutive successes in half-open that close it.
	SuccessThreshold int

	// OpenDuration is how long the circuit stays open before probing.
	OpenDuration time.Duration

	Name    string
	Logger  *slog.Logger
	Metrics *observability.Metrics
}

func (c *CircuitBreakerConfig) withDefaults() CircuitBreakerConfig {
	out := *c
	if out.FailureThreshold == 0 {
		out.FailureThreshold = 5
	}
	if out.SuccessThreshold == 0 {
		out.SuccessThreshold = 2
	}
	if out.OpenDuration == 0 {
		out.OpenDuration = 60 * time.Second
	}
	if out.Name == "" {
		out.Name = "default"
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return out
}

// CircuitBreaker implements ResiliencePolicy as an FSM-based circuit breaker.
//
// FSM transitions:
//
//	Closed ──[≥FailureThreshold failures]──► Open
//	Open   ──[OpenDuration elapsed]────────► HalfOpen
//	HalfOpen ──[success]────────────────────► Closed
//	HalfOpen ──[failure]────────────────────► Open
type CircuitBreaker struct {
	cfg              CircuitBreakerConfig
	mu               sync.Mutex
	state            CircuitState
	consecutiveFails int
	consecutiveOK    int
	openedAt         time.Time
}

// NewCircuitBreaker constructs a CircuitBreaker in the Closed state.
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{cfg: cfg.withDefaults(), state: CircuitClosed}
}

// ErrCircuitOpen is returned when calls are rejected due to an open circuit.
type ErrCircuitOpen struct{ Name string }

func (e ErrCircuitOpen) Error() string {
	return fmt.Sprintf("circuit breaker %q is open", e.Name)
}

// Execute applies circuit-breaking logic around fn.
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := cb.allow(); err != nil {
		return err
	}

	err := fn(ctx)
	cb.record(err)
	return err
}

// State returns the current circuit state (thread-safe snapshot).
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

func (cb *CircuitBreaker) allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return nil
	case CircuitOpen:
		if time.Since(cb.openedAt) >= cb.cfg.OpenDuration {
			cb.transition(CircuitHalfOpen)
			return nil
		}
		return ErrCircuitOpen{Name: cb.cfg.Name}
	case CircuitHalfOpen:
		return nil // let the probe through
	}
	return nil
}

func (cb *CircuitBreaker) record(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.consecutiveOK = 0
		cb.consecutiveFails++
		if cb.state == CircuitHalfOpen || cb.consecutiveFails >= cb.cfg.FailureThreshold {
			cb.transition(CircuitOpen)
		}
	} else {
		cb.consecutiveFails = 0
		cb.consecutiveOK++
		if cb.state == CircuitHalfOpen && cb.consecutiveOK >= cb.cfg.SuccessThreshold {
			cb.transition(CircuitClosed)
		}
	}
}

func (cb *CircuitBreaker) transition(next CircuitState) {
	prev := cb.state
	cb.state = next
	if next == CircuitOpen {
		cb.openedAt = time.Now()
		cb.consecutiveOK = 0
	}
	cb.cfg.Logger.Info("circuit breaker state transition",
		"name", cb.cfg.Name,
		"from", prev.String(),
		"to", next.String(),
	)
	if cb.cfg.Metrics != nil {
		cb.cfg.Metrics.CircuitBreakerState.WithLabelValues(
			cb.cfg.Name, next.String()).Set(1)
		cb.cfg.Metrics.CircuitBreakerState.WithLabelValues(
			cb.cfg.Name, prev.String()).Set(0)
	}
}

// ComposedPolicy chains a CircuitBreaker around a RetryPolicy.
// The circuit breaker sees only one "call" per Execute invocation; the retry
// policy drives multiple internal attempts. This prevents the breaker from
// counting each retry attempt as a separate failure.
type ComposedPolicy struct {
	cb    *CircuitBreaker
	retry *RetryPolicy
}

// NewComposedPolicy composes a circuit breaker (outer) with retry (inner).
func NewComposedPolicy(cb *CircuitBreaker, retry *RetryPolicy) *ComposedPolicy {
	return &ComposedPolicy{cb: cb, retry: retry}
}

func (p *ComposedPolicy) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	return p.cb.Execute(ctx, func(innerCtx context.Context) error {
		return p.retry.Execute(innerCtx, fn)
	})
}
