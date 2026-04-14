package event_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/core/content/event"
)

func TestBus_PublishDeliveresToAllHandlers(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	var (
		mu      sync.Mutex
		received []event.Event
	)

	bus.Subscribe(func(_ context.Context, e event.Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})
	bus.Subscribe(func(_ context.Context, e event.Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	evt := event.Event{Kind: event.KindWriteStarted, Source: "test"}
	bus.Publish(ctx, evt)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(received))
	}
}

func TestBus_UnsubscribeStopsDelivery(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	var count atomic.Int64
	unsub := bus.Subscribe(func(_ context.Context, _ event.Event) {
		count.Add(1)
	})

	bus.Publish(ctx, event.Event{Kind: event.KindReadHit})
	unsub()
	bus.Publish(ctx, event.Event{Kind: event.KindReadHit})

	if got := count.Load(); got != 1 {
		t.Fatalf("expected handler to be called once, got %d", got)
	}
}

func TestBus_UnsubscribeIsIdempotent(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	unsub := bus.Subscribe(func(_ context.Context, _ event.Event) {})

	// Should not panic or deadlock when called multiple times.
	unsub()
	unsub()
	unsub()
}

func TestBus_PublishAsyncDelivers(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	done := make(chan struct{})
	bus.Subscribe(func(_ context.Context, _ event.Event) {
		close(done)
	})

	bus.PublishAsync(ctx, event.Event{Kind: event.KindWriteCommitted})

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("async handler not called within timeout")
	}
}

func TestBus_PublishAsyncWithNoHandlers(t *testing.T) {
	t.Parallel()

	// Should not panic when there are no handlers.
	bus := event.NewBus()
	bus.PublishAsync(context.Background(), event.Event{Kind: event.KindReadMiss})
}

func TestBus_ConcurrentSubscribePublish(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	var wg sync.WaitGroup
	const goroutines = 50

	// Concurrently subscribe, publish, and unsubscribe.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unsub := bus.Subscribe(func(_ context.Context, _ event.Event) {})
			bus.Publish(ctx, event.Event{Kind: event.KindReadHit})
			unsub()
		}()
	}

	wg.Wait()
}

func TestBus_EventFieldsArePreserved(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	want := event.Event{
		Kind:       event.KindWriteCommitted,
		Source:     "mystore",
		Ref:        "myref",
		OccurredAt: time.Now().Round(time.Millisecond),
	}

	got := make(chan event.Event, 1)
	bus.Subscribe(func(_ context.Context, e event.Event) { got <- e })
	bus.Publish(ctx, want)

	select {
	case e := <-got:
		if e.Kind != want.Kind {
			t.Errorf("Kind: got %q, want %q", e.Kind, want.Kind)
		}
		if e.Source != want.Source {
			t.Errorf("Source: got %q, want %q", e.Source, want.Source)
		}
		if e.Ref != want.Ref {
			t.Errorf("Ref: got %q, want %q", e.Ref, want.Ref)
		}
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
}
