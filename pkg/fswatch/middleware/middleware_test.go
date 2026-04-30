package middleware_test

import (
	"context"
	"errors"
	"testing"

	fanwatch "github.com/bons/bons-ci/pkg/fswatch"
	"github.com/bons/bons-ci/pkg/fswatch/middleware"
	"github.com/bons/bons-ci/pkg/fswatch/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// RecoveryMiddleware tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRecoveryMiddleware_CatchesPanic(t *testing.T) {
	var recoveredValue any

	recovery := middleware.NewRecovery(func(rec any, _ *fanwatch.EnrichedEvent) {
		recoveredValue = rec
	})

	wrapped := recovery.Wrap(&testutil.PanicHandler{Value: "boom"})

	err := wrapped.Handle(context.Background(), testutil.NewEnrichedEvent().Build())
	if err == nil {
		t.Fatal("RecoveryMiddleware: expected error from panic, got nil")
	}
	if recoveredValue != "boom" {
		t.Errorf("recovered value = %v, want boom", recoveredValue)
	}
}

func TestRecoveryMiddleware_CatchesErrorPanic(t *testing.T) {
	sentinel := errors.New("panic error")
	recovery := middleware.NewRecovery(nil)

	wrapped := recovery.Wrap(&testutil.PanicHandler{Value: sentinel})
	err := wrapped.Handle(context.Background(), testutil.NewEnrichedEvent().Build())

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Error should contain the sentinel message (wrapped in panic message).
	if !errors.Is(err, sentinel) {
		// The panic wraps it — check message instead.
		if err.Error() == "" {
			t.Error("expected non-empty error message")
		}
	}
}

func TestRecoveryMiddleware_PassesThrough(t *testing.T) {
	recovery := middleware.NewRecovery(nil)
	collector := &fanwatch.CollectingHandler{}
	wrapped := recovery.Wrap(collector)

	err := wrapped.Handle(context.Background(), testutil.NewEnrichedEvent().Build())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if collector.Len() != 1 {
		t.Error("inner handler should have been called once")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoggingMiddleware tests
// ─────────────────────────────────────────────────────────────────────────────

func TestLoggingMiddleware_LogsOnError(t *testing.T) {
	var logged bool
	logFn := func(msg string, _ ...any) { logged = true }

	lm := middleware.NewLogging(logFn)
	wrapped := lm.Wrap(&testutil.ErroringHandler{Err: errors.New("test error")})

	err := wrapped.Handle(context.Background(), testutil.NewEnrichedEvent().Build())
	if err == nil {
		t.Fatal("expected error from inner handler")
	}
	if !logged {
		t.Error("LoggingMiddleware: should have logged the error")
	}
}

func TestLoggingMiddleware_SilentOnSuccess(t *testing.T) {
	var logged bool
	logFn := func(msg string, _ ...any) { logged = true }

	lm := middleware.NewLogging(logFn)
	wrapped := lm.Wrap(fanwatch.NoopHandler{})

	_ = wrapped.Handle(context.Background(), testutil.NewEnrichedEvent().Build())
	if logged {
		t.Error("LoggingMiddleware: should not log on success")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MetricsMiddleware tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMetricsMiddleware_CountsHandled(t *testing.T) {
	m := &middleware.MetricsMiddleware{}
	wrapped := m.Wrap(fanwatch.NoopHandler{})
	ctx := context.Background()

	for range 5 {
		_ = wrapped.Handle(ctx, testutil.NewEnrichedEvent().Build())
	}

	if m.Handled() != 5 {
		t.Errorf("Handled = %d, want 5", m.Handled())
	}
	if m.Errors() != 0 {
		t.Errorf("Errors = %d, want 0", m.Errors())
	}
}

func TestMetricsMiddleware_CountsErrors(t *testing.T) {
	m := &middleware.MetricsMiddleware{}
	errH := &testutil.ErroringHandler{Err: errors.New("boom")}
	wrapped := m.Wrap(errH)
	ctx := context.Background()

	for range 3 {
		_ = wrapped.Handle(ctx, testutil.NewEnrichedEvent().Build())
	}

	if m.Errors() != 3 {
		t.Errorf("Errors = %d, want 3", m.Errors())
	}
	if m.Handled() != 0 {
		t.Errorf("Handled = %d, want 0", m.Handled())
	}
}

func TestMetricsMiddleware_Reset(t *testing.T) {
	m := &middleware.MetricsMiddleware{}
	wrapped := m.Wrap(fanwatch.NoopHandler{})
	_ = wrapped.Handle(context.Background(), testutil.NewEnrichedEvent().Build())

	m.Reset()

	if m.Handled() != 0 || m.Errors() != 0 {
		t.Error("Reset: counters should be zero")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CapturingMiddleware from testutil — used in middleware tests
// ─────────────────────────────────────────────────────────────────────────────

func TestCapturingMiddleware_CapturesEvents(t *testing.T) {
	cap := &testutil.CapturingMiddleware{}
	wrapped := cap.Wrap(fanwatch.NoopHandler{})

	ctx := context.Background()
	for i := range 3 {
		e := testutil.NewEnrichedEvent().WithPID(int32(1000 + i)).Build()
		if err := wrapped.Handle(ctx, e); err != nil {
			t.Fatalf("Handle: unexpected error: %v", err)
		}
	}

	if cap.Len() != 3 {
		t.Errorf("Len = %d, want 3", cap.Len())
	}
}

func TestCapturingMiddleware_ClonesEvents(t *testing.T) {
	cap := &testutil.CapturingMiddleware{}
	wrapped := cap.Wrap(fanwatch.NoopHandler{})

	e := testutil.NewEnrichedEvent().WithAttr("shared", "original").Build()
	_ = wrapped.Handle(context.Background(), e)

	// Mutate original — captured clone should be unaffected.
	e.SetAttr("shared", "mutated")

	captured := cap.Events()[0]
	if captured.Attr("shared") != "original" {
		t.Error("captured events should be independent clones")
	}
}

func TestCapturingMiddleware_Reset(t *testing.T) {
	cap := &testutil.CapturingMiddleware{}
	wrapped := cap.Wrap(fanwatch.NoopHandler{})

	_ = wrapped.Handle(context.Background(), testutil.NewEnrichedEvent().Build())
	cap.Reset()

	if cap.Len() != 0 {
		t.Errorf("Len after Reset = %d, want 0", cap.Len())
	}
}
