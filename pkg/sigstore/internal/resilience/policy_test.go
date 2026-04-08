package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/resilience"
)

// ══════════════════════════════════════════════════════════════════════════════
// RetryPolicy tests
// ══════════════════════════════════════════════════════════════════════════════

func TestRetryPolicy_SucceedsOnFirstAttempt(t *testing.T) {
	policy := resilience.NewRetryPolicy(resilience.RetryConfig{
		MaxAttempts: 3,
		InitialWait: time.Millisecond,
	})
	calls := 0
	err := policy.Execute(context.Background(), func(_ context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestRetryPolicy_RetriesUpToMaxAttempts(t *testing.T) {
	policy := resilience.NewRetryPolicy(resilience.RetryConfig{
		MaxAttempts: 3,
		InitialWait: time.Millisecond,
		MaxWait:     5 * time.Millisecond,
	})
	calls := 0
	sentinel := errors.New("always fails")
	err := policy.Execute(context.Background(), func(_ context.Context) error {
		calls++
		return sentinel
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want %d (MaxAttempts)", calls, 3)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain does not contain sentinel: %v", err)
	}
}

func TestRetryPolicy_SucceedsOnNthAttempt(t *testing.T) {
	tests := []struct {
		name        string
		succeedOn   int
		maxAttempts int
		wantErr     bool
	}{
		{name: "succeeds on 2nd of 3", succeedOn: 2, maxAttempts: 3, wantErr: false},
		{name: "succeeds on 3rd of 3", succeedOn: 3, maxAttempts: 3, wantErr: false},
		{name: "fails all 3", succeedOn: 4, maxAttempts: 3, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := resilience.NewRetryPolicy(resilience.RetryConfig{
				MaxAttempts: tt.maxAttempts,
				InitialWait: time.Millisecond,
				MaxWait:     5 * time.Millisecond,
			})
			calls := 0
			err := policy.Execute(context.Background(), func(_ context.Context) error {
				calls++
				if calls >= tt.succeedOn {
					return nil
				}
				return errors.New("transient")
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("wantErr=%v, got err=%v", tt.wantErr, err)
			}
		})
	}
}

func TestRetryPolicy_NonRetryableErrorStopsImmediately(t *testing.T) {
	policy := resilience.NewRetryPolicy(resilience.RetryConfig{
		MaxAttempts: 5,
		InitialWait: time.Millisecond,
		IsRetryable: func(err error) bool {
			return !errors.Is(err, context.Canceled)
		},
	})
	calls := 0
	err := policy.Execute(context.Background(), func(_ context.Context) error {
		calls++
		return context.Canceled
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (non-retryable should stop immediately)", calls)
	}
}

func TestRetryPolicy_ContextCancelledDuringWait(t *testing.T) {
	policy := resilience.NewRetryPolicy(resilience.RetryConfig{
		MaxAttempts: 10,
		InitialWait: 500 * time.Millisecond, // long wait to catch cancellation
		MaxWait:     1 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	calls := 0
	err := policy.Execute(ctx, func(_ context.Context) error {
		calls++
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should have attempted once and then been cancelled during the wait
	if calls > 2 {
		t.Errorf("calls = %d, expected ≤2 before context cancelled", calls)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// CircuitBreaker tests
// ══════════════════════════════════════════════════════════════════════════════

func TestCircuitBreaker_InitiallyClosed(t *testing.T) {
	cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenDuration:     time.Second,
	})
	if got := cb.State(); got != resilience.CircuitClosed {
		t.Errorf("initial state = %v, want Closed", got)
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	const threshold = 3
	cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
		FailureThreshold: threshold,
		OpenDuration:     time.Minute,
	})
	for i := 0; i < threshold; i++ {
		_ = cb.Execute(context.Background(), func(_ context.Context) error {
			return errors.New("fail")
		})
	}
	if got := cb.State(); got != resilience.CircuitOpen {
		t.Errorf("state after %d failures = %v, want Open", threshold, got)
	}
}

func TestCircuitBreaker_RejectsCallsWhenOpen(t *testing.T) {
	cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
		FailureThreshold: 1,
		OpenDuration:     time.Minute,
	})
	// Trip the breaker
	_ = cb.Execute(context.Background(), func(_ context.Context) error {
		return errors.New("fail")
	})

	called := false
	err := cb.Execute(context.Background(), func(_ context.Context) error {
		called = true
		return nil
	})
	if called {
		t.Error("fn was called with open circuit")
	}
	var openErr resilience.ErrCircuitOpen
	if !errors.As(err, &openErr) {
		t.Errorf("expected ErrCircuitOpen, got %T: %v", err, err)
	}
}

func TestCircuitBreaker_HalfOpenAfterOpenDuration(t *testing.T) {
	cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenDuration:     50 * time.Millisecond,
	})
	_ = cb.Execute(context.Background(), func(_ context.Context) error {
		return errors.New("fail")
	})
	if cb.State() != resilience.CircuitOpen {
		t.Fatal("expected Open state")
	}

	time.Sleep(100 * time.Millisecond) // wait for open duration

	// The next call should be allowed through (half-open probe)
	_ = cb.Execute(context.Background(), func(_ context.Context) error {
		return nil // probe succeeds
	})
	if got := cb.State(); got != resilience.CircuitClosed {
		t.Errorf("state after successful probe = %v, want Closed", got)
	}
}

func TestCircuitBreaker_FullCycle(t *testing.T) {
	tests := []struct {
		name      string
		actions   []bool // true=success, false=fail
		wantState resilience.CircuitState
	}{
		{
			name:      "3 fails → open",
			actions:   []bool{false, false, false},
			wantState: resilience.CircuitOpen,
		},
		{
			name:      "2 fails → still closed",
			actions:   []bool{false, false},
			wantState: resilience.CircuitClosed,
		},
		{
			name:      "fail then succeed → resets counter",
			actions:   []bool{false, true, false, true},
			wantState: resilience.CircuitClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
				FailureThreshold: 3,
				SuccessThreshold: 2,
				OpenDuration:     time.Minute,
			})
			for _, succeed := range tt.actions {
				_ = cb.Execute(context.Background(), func(_ context.Context) error {
					if succeed {
						return nil
					}
					return errors.New("fail")
				})
			}
			if got := cb.State(); got != tt.wantState {
				t.Errorf("state = %v, want %v", got, tt.wantState)
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// ComposedPolicy tests
// ══════════════════════════════════════════════════════════════════════════════

func TestComposedPolicy_CircuitOpenBlocksRetry(t *testing.T) {
	cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
		FailureThreshold: 1,
		OpenDuration:     time.Minute,
	})
	retry := resilience.NewRetryPolicy(resilience.RetryConfig{
		MaxAttempts: 5,
		InitialWait: time.Millisecond,
	})
	policy := resilience.NewComposedPolicy(cb, retry)

	calls := 0
	// First execute trips the circuit breaker (retry exhausted counts as 1 CB failure)
	_ = policy.Execute(context.Background(), func(_ context.Context) error {
		calls++
		return errors.New("fail")
	})

	callsAfterTrip := calls
	// Second execute should be rejected by the open circuit
	err := policy.Execute(context.Background(), func(_ context.Context) error {
		calls++
		return nil
	})

	var openErr resilience.ErrCircuitOpen
	if !errors.As(err, &openErr) {
		t.Errorf("expected ErrCircuitOpen on second execute, got %T: %v", err, err)
	}
	if calls != callsAfterTrip {
		t.Errorf("fn was called after circuit opened: calls before=%d, after=%d",
			callsAfterTrip, calls)
	}
}
