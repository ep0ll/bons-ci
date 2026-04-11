package events_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/policy/opa/internal/events"
	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { polOtel.UseNoop() }

func newBus(t *testing.T) *events.Bus {
	t.Helper()
	b, err := events.NewBus(nil)
	require.NoError(t, err)
	return b
}

// ─── Construction ─────────────────────────────────────────────────────────────

func TestNewBus_NilErrorHandler(t *testing.T) {
	b, err := events.NewBus(nil)
	require.NoError(t, err)
	require.NotNil(t, b)
	// Publishing to no subscribers should not panic.
	b.Publish(context.Background(), events.RawEvent{Kind: "test"})
}

// ─── Subscribe / Publish ──────────────────────────────────────────────────────

func TestBus_BasicDelivery(t *testing.T) {
	b := newBus(t)
	var got []string
	sub := b.Subscribe("a", func(_ context.Context, e events.RawEvent) error {
		got = append(got, e.Payload.(string))
		return nil
	})
	defer sub.Cancel()

	b.Publish(context.Background(), events.RawEvent{Kind: "a", Payload: "hello"})
	b.Publish(context.Background(), events.RawEvent{Kind: "a", Payload: "world"})
	b.Publish(context.Background(), events.RawEvent{Kind: "b", Payload: "ignored"})
	assert.Equal(t, []string{"hello", "world"}, got)
}

func TestBus_WildcardSubscription(t *testing.T) {
	b := newBus(t)
	var count int
	sub := b.Subscribe("", func(_ context.Context, _ events.RawEvent) error {
		count++
		return nil
	})
	defer sub.Cancel()
	b.Publish(context.Background(), events.RawEvent{Kind: "x"})
	b.Publish(context.Background(), events.RawEvent{Kind: "y"})
	b.Publish(context.Background(), events.RawEvent{Kind: "z"})
	assert.Equal(t, 3, count)
}

func TestBus_MultipleSubscribers_AllReceive(t *testing.T) {
	b := newBus(t)
	var c1, c2, c3 int
	b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error { c1++; return nil })
	b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error { c2++; return nil })
	b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error { c3++; return nil })
	b.Publish(context.Background(), events.RawEvent{Kind: "ev"})
	assert.Equal(t, 1, c1)
	assert.Equal(t, 1, c2)
	assert.Equal(t, 1, c3)
}

func TestBus_Cancel_StopsDelivery(t *testing.T) {
	b := newBus(t)
	var count int
	sub := b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error {
		count++
		return nil
	})
	b.Publish(context.Background(), events.RawEvent{Kind: "ev"})
	assert.Equal(t, 1, count)
	sub.Cancel()
	b.Publish(context.Background(), events.RawEvent{Kind: "ev"})
	assert.Equal(t, 1, count, "handler must not fire after Cancel")
}

func TestBus_Cancel_Idempotent(t *testing.T) {
	b := newBus(t)
	sub := b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error { return nil })
	// Must not panic on double-cancel.
	sub.Cancel()
	sub.Cancel()
	sub.Cancel()
}

func TestBus_HandlerError_CallsOnErr(t *testing.T) {
	errCh := make(chan error, 1)
	b, err := events.NewBus(func(err error, _ events.RawEvent) { errCh <- err })
	require.NoError(t, err)

	sentinel := errors.New("handler error")
	b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error { return sentinel })
	b.Publish(context.Background(), events.RawEvent{Kind: "ev"})

	select {
	case got := <-errCh:
		assert.Equal(t, sentinel, got)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("onErr not called")
	}
}

func TestBus_HandlerError_DoesNotStopOtherHandlers(t *testing.T) {
	b := newBus(t)
	var secondCalled bool
	b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error {
		return errors.New("first fails")
	})
	b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error {
		secondCalled = true
		return nil
	})
	b.Publish(context.Background(), events.RawEvent{Kind: "ev"})
	assert.True(t, secondCalled)
}

func TestBus_HandlerPanic_Recovered(t *testing.T) {
	var errCalled bool
	b, err := events.NewBus(func(_ error, _ events.RawEvent) { errCalled = true })
	require.NoError(t, err)

	b.Subscribe("ev", func(_ context.Context, _ events.RawEvent) error {
		panic("BOOM")
	})
	// Must not panic the test.
	b.Publish(context.Background(), events.RawEvent{Kind: "ev"})
	assert.True(t, errCalled)
}

// ─── Typed helpers ────────────────────────────────────────────────────────────

func TestPublishTyped_TypedDelivery(t *testing.T) {
	b := newBus(t)
	type payload struct{ X int }
	var got payload
	sub := events.On[payload](b, "typed", func(_ context.Context, p payload) error {
		got = p
		return nil
	})
	defer sub.Cancel()

	events.PublishTyped(context.Background(), b, events.Event[payload]{
		Kind:    "typed",
		Payload: payload{X: 42},
	})
	assert.Equal(t, 42, got.X)
}

func TestOn_WrongPayloadType_Skipped(t *testing.T) {
	b := newBus(t)
	var called bool
	sub := events.On[string](b, "ev", func(_ context.Context, _ string) error {
		called = true
		return nil
	})
	defer sub.Cancel()

	// Publish with int payload — type assertion to string fails → skipped.
	b.Publish(context.Background(), events.RawEvent{Kind: "ev", Payload: 123})
	assert.False(t, called)
}

// ─── Handler combinators ──────────────────────────────────────────────────────

func TestPipeline_ExecutesInOrder(t *testing.T) {
	var order []int
	h := events.Pipeline(
		func(_ context.Context, _ events.RawEvent) error { order = append(order, 1); return nil },
		func(_ context.Context, _ events.RawEvent) error { order = append(order, 2); return nil },
		func(_ context.Context, _ events.RawEvent) error { order = append(order, 3); return nil },
	)
	require.NoError(t, h(context.Background(), events.RawEvent{}))
	assert.Equal(t, []int{1, 2, 3}, order)
}

func TestPipeline_StopsOnError(t *testing.T) {
	var ran []int
	sentinel := errors.New("stop")
	h := events.Pipeline(
		func(_ context.Context, _ events.RawEvent) error { ran = append(ran, 1); return nil },
		func(_ context.Context, _ events.RawEvent) error { ran = append(ran, 2); return sentinel },
		func(_ context.Context, _ events.RawEvent) error { ran = append(ran, 3); return nil },
	)
	err := h(context.Background(), events.RawEvent{})
	assert.Equal(t, sentinel, err)
	assert.Equal(t, []int{1, 2}, ran)
}

func TestFilter_PredicateTrue_Fires(t *testing.T) {
	var called bool
	h := events.Filter(
		func(e events.RawEvent) bool { return e.Kind == "yes" },
		func(_ context.Context, _ events.RawEvent) error { called = true; return nil },
	)
	h(context.Background(), events.RawEvent{Kind: "yes"}) //nolint
	assert.True(t, called)
}

func TestFilter_PredicateFalse_DoesNotFire(t *testing.T) {
	var called bool
	h := events.Filter(
		func(e events.RawEvent) bool { return e.Kind == "yes" },
		func(_ context.Context, _ events.RawEvent) error { called = true; return nil },
	)
	h(context.Background(), events.RawEvent{Kind: "no"}) //nolint
	assert.False(t, called)
}

func TestRetry_SucceedsOnSecondAttempt(t *testing.T) {
	attempts := 0
	h := events.Retry(3, time.Millisecond, func(_ context.Context, _ events.RawEvent) error {
		attempts++
		if attempts < 2 {
			return errors.New("fail")
		}
		return nil
	})
	err := h(context.Background(), events.RawEvent{})
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
}

func TestRetry_ExhaustsAllAttempts(t *testing.T) {
	h := events.Retry(3, time.Millisecond, func(_ context.Context, _ events.RawEvent) error {
		return errors.New("always fails")
	})
	err := h(context.Background(), events.RawEvent{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retry exhausted")
}

func TestRetry_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	h := events.Retry(10, time.Hour, func(_ context.Context, _ events.RawEvent) error {
		attempts++
		return errors.New("fail")
	})
	err := h(ctx, events.RawEvent{})
	require.Error(t, err)
	// Should have attempted once then noticed cancelled context.
	assert.LessOrEqual(t, attempts, 2)
}

// ─── Channel bridge ───────────────────────────────────────────────────────────

func TestChan_ReceivesEvents(t *testing.T) {
	b := newBus(t)
	ctx, ctxCancel := context.WithTimeout(context.Background(), time.Second)
	defer ctxCancel()
	ch, cancel := events.Chan(ctx, b, "stream", 10)
	defer cancel()

	b.Publish(context.Background(), events.RawEvent{Kind: "stream", Payload: "a"})
	b.Publish(context.Background(), events.RawEvent{Kind: "stream", Payload: "b"})

	got := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case e := <-ch:
			got = append(got, e.Payload.(string))
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timeout waiting for event")
		}
	}
	assert.Equal(t, []string{"a", "b"}, got)
}

// ─── Concurrency ─────────────────────────────────────────────────────────────

func TestBus_ConcurrentPublishAndSubscribe(t *testing.T) {
	b := newBus(t)
	var received atomic.Int64
	const publishers = 10
	const eventsEach = 100

	sub := b.Subscribe("concurrent", func(_ context.Context, _ events.RawEvent) error {
		received.Add(1)
		return nil
	})
	defer sub.Cancel()

	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsEach; j++ {
				b.Publish(context.Background(), events.RawEvent{Kind: "concurrent"})
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(publishers*eventsEach), received.Load())
}

func TestBus_ConcurrentSubscribeCancel(t *testing.T) {
	b := newBus(t)
	const goroutines = 20
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := b.Subscribe("x", func(_ context.Context, _ events.RawEvent) error { return nil })
			b.Publish(context.Background(), events.RawEvent{Kind: "x"})
			sub.Cancel()
			sub.Cancel() // idempotent under concurrency
		}()
	}
	wg.Wait() // must not deadlock or panic
}

// ─── Timestamp injection ──────────────────────────────────────────────────────

func TestBus_TimestampInjected_WhenZero(t *testing.T) {
	b := newBus(t)
	var got events.RawEvent
	b.Subscribe("ts", func(_ context.Context, e events.RawEvent) error {
		got = e
		return nil
	})
	before := time.Now()
	b.Publish(context.Background(), events.RawEvent{Kind: "ts"}) // zero timestamp
	after := time.Now()
	assert.False(t, got.Timestamp.IsZero())
	assert.True(t, got.Timestamp.After(before) || got.Timestamp.Equal(before))
	assert.True(t, got.Timestamp.Before(after) || got.Timestamp.Equal(after))
}
