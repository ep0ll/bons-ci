package reactdag

import (
	"context"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// RetryExecutor
// ---------------------------------------------------------------------------

// RetryExecutor wraps an inner Executor and applies per-vertex retry and
// timeout policies. It reads the RetryPolicy from the vertex's "retry" label
// namespace or falls back to the global defaults set at construction time.
//
// Retry semantics:
//   - If the operation succeeds on any attempt, the vertex is marked completed.
//   - If all attempts are exhausted, the last error is propagated.
//   - The error is stored in cache after all retries fail, so the next build
//     with unchanged inputs replays the cached failure without retrying.
type RetryExecutor struct {
	inner          Executor
	defaultPolicy  RetryPolicy
	defaultTimeout time.Duration
}

// NewRetryExecutor constructs a RetryExecutor.
// defaultTimeout of 0 means no per-vertex timeout.
// defaultPolicy.MaxAttempts of 0 or 1 means no retry.
func NewRetryExecutor(inner Executor, defaultTimeout time.Duration, defaultPolicy RetryPolicy) *RetryExecutor {
	return &RetryExecutor{
		inner:          inner,
		defaultPolicy:  defaultPolicy,
		defaultTimeout: defaultTimeout,
	}
}

// Execute runs the inner executor with retry and timeout applied.
func (e *RetryExecutor) Execute(ctx context.Context, v *Vertex) error {
	policy := e.policyFor(v)
	timeout := e.timeoutFor(v)

	maxAttempts := policy.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		execCtx, cancel := e.withTimeout(ctx, timeout)
		lastErr = e.inner.Execute(execCtx, v)
		cancel()

		if lastErr == nil {
			return nil
		}

		if !policy.ShouldRetry(attempt, lastErr) {
			break
		}

		delay := policy.DelayFor(attempt)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return fmt.Errorf("vertex %q: context cancelled during retry backoff: %w", v.ID(), ctx.Err())
			}
		}
	}
	return lastErr
}

// policyFor returns the retry policy for a vertex.
// Vertices may override the global default by carrying a RetryPolicy in their
// spec (stored via the builder's WithRetry option).
func (e *RetryExecutor) policyFor(v *Vertex) RetryPolicy {
	// In the builder-based flow, the spec's RetryPolicy is embedded in the
	// vertex operation if it implements RetryPolicyProvider. For label-based
	// flow, check a "retry_attempts" label.
	if rp, ok := v.Op().(RetryPolicyProvider); ok {
		return rp.RetryPolicy()
	}
	return e.defaultPolicy
}

// timeoutFor returns the per-vertex execution timeout.
func (e *RetryExecutor) timeoutFor(v *Vertex) time.Duration {
	if label, ok := v.Label("timeout"); ok {
		if d, err := time.ParseDuration(label); err == nil {
			return d
		}
	}
	return e.defaultTimeout
}

// withTimeout wraps ctx with a deadline if d > 0, otherwise returns ctx as-is.
func (e *RetryExecutor) withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// ---------------------------------------------------------------------------
// RetryPolicyProvider interface
// ---------------------------------------------------------------------------

// RetryPolicyProvider is an optional interface that an Operation can implement
// to carry its own RetryPolicy rather than relying on the global default.
type RetryPolicyProvider interface {
	RetryPolicy() RetryPolicy
}

// ---------------------------------------------------------------------------
// ResourceLimiter — semaphore-based concurrency control per resource class
// ---------------------------------------------------------------------------

// ResourceLimiter constrains concurrent vertex execution per named resource
// class. For example: limiting GPU-bound vertices to 2 concurrent executions
// while allowing unlimited CPU-bound vertices.
//
// Vertices declare their resource class via the "resource_class" label.
// Unlabelled vertices are unconstrained.
type ResourceLimiter struct {
	inner   Executor
	semaphs map[string]chan struct{} // resource class → semaphore tokens
}

// NewResourceLimiter creates a ResourceLimiter that wraps inner.
// limits maps resource class names to their maximum concurrency.
func NewResourceLimiter(inner Executor, limits map[string]int) *ResourceLimiter {
	semaphs := make(map[string]chan struct{}, len(limits))
	for class, n := range limits {
		semaphs[class] = make(chan struct{}, n)
		for range n {
			semaphs[class] <- struct{}{} // pre-fill tokens
		}
	}
	return &ResourceLimiter{inner: inner, semaphs: semaphs}
}

// Execute acquires a semaphore token for the vertex's resource class (if any),
// runs the inner executor, then releases the token.
func (r *ResourceLimiter) Execute(ctx context.Context, v *Vertex) error {
	class, hasClass := v.Label("resource_class")
	if !hasClass {
		return r.inner.Execute(ctx, v)
	}

	sem, ok := r.semaphs[class]
	if !ok {
		return r.inner.Execute(ctx, v) // unknown class → unconstrained
	}

	// Acquire token.
	select {
	case <-sem:
	case <-ctx.Done():
		return fmt.Errorf("vertex %q: context cancelled waiting for resource %q: %w",
			v.ID(), class, ctx.Err())
	}
	defer func() { sem <- struct{}{} }() // release token

	return r.inner.Execute(ctx, v)
}
