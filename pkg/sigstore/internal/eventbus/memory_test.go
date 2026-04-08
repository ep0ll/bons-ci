package eventbus_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
	"github.com/bons/bons-ci/pkg/sigstore/internal/eventbus"
)

func newTestBus(t *testing.T, cfg eventbus.MemoryBusConfig) *eventbus.MemoryBus {
	t.Helper()
	if cfg.BufferSize == 0 {
		cfg.BufferSize = 32
	}
	if cfg.WorkersPerTopic == 0 {
		cfg.WorkersPerTopic = 2
	}
	if cfg.HandlerTimeout == 0 {
		cfg.HandlerTimeout = 2 * time.Second
	}
	bus := eventbus.NewMemoryBus(cfg)
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func makeEnvelope(topic domain.EventType) domain.Envelope {
	return domain.Envelope{
		Topic: topic,
		Payload: domain.SigningRequestedEvent{
			BaseEvent: domain.BaseEvent{
				ID:      "test-id",
				Type:    topic,
				Version: 1,
			},
			ImageRef: "reg/img@sha256:000",
		},
	}
}

func TestMemoryBus_PublishAndReceive(t *testing.T) {
	bus := newTestBus(t, eventbus.MemoryBusConfig{})
	received := make(chan domain.Envelope, 1)

	_, err := bus.Subscribe(domain.EventTypeSigningRequested, func(_ context.Context, env domain.Envelope) error {
		received <- env
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	env := makeEnvelope(domain.EventTypeSigningRequested)
	if err = bus.Publish(context.Background(), env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case got := <-received:
		if got.Topic != domain.EventTypeSigningRequested {
			t.Errorf("topic = %q, want %q", got.Topic, domain.EventTypeSigningRequested)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: event not received")
	}
}

func TestMemoryBus_FanOut_MultipleSubscribers(t *testing.T) {
	const numSubscribers = 5
	bus := newTestBus(t, eventbus.MemoryBusConfig{})

	var received atomic.Int64
	for i := 0; i < numSubscribers; i++ {
		_, err := bus.Subscribe(domain.EventTypeSigningRequested, func(_ context.Context, _ domain.Envelope) error {
			received.Add(1)
			return nil
		})
		if err != nil {
			t.Fatalf("Subscribe %d: %v", i, err)
		}
	}

	_ = bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))

	// Wait for all subscribers to process
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() == numSubscribers {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("received %d events, want %d", received.Load(), numSubscribers)
}

func TestMemoryBus_Backpressure_ReturnsBusFull(t *testing.T) {
	// Buffer of 1, blocking handler → second publish hits backpressure
	bus := newTestBus(t, eventbus.MemoryBusConfig{
		BufferSize:      1,
		WorkersPerTopic: 1,
		HandlerTimeout:  10 * time.Second,
	})

	blocked := make(chan struct{})
	unblock := make(chan struct{})

	_, _ = bus.Subscribe(domain.EventTypeSigningRequested, func(_ context.Context, _ domain.Envelope) error {
		close(blocked)
		<-unblock
		return nil
	})

	// First event: enters buffer
	_ = bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))

	// Wait for handler to start (buffer now empty but worker blocked)
	<-blocked

	// Fill the buffer
	_ = bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))

	// Next publish should hit backpressure
	err := bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))
	if err == nil {
		close(unblock)
		t.Fatal("expected ErrBusFull, got nil")
	}
	var busFull eventbus.ErrBusFull
	if !errors.As(err, &busFull) {
		close(unblock)
		t.Errorf("expected ErrBusFull, got %T: %v", err, err)
	}
	close(unblock)
}

func TestMemoryBus_TopicIsolation(t *testing.T) {
	bus := newTestBus(t, eventbus.MemoryBusConfig{})
	var mu sync.Mutex
	receivedTopics := make(map[domain.EventType]int)

	for _, topic := range []domain.EventType{
		domain.EventTypeSigningRequested,
		domain.EventTypeSigningSucceeded,
		domain.EventTypeSigningFailed,
	} {
		topic := topic // capture
		_, _ = bus.Subscribe(topic, func(_ context.Context, env domain.Envelope) error {
			mu.Lock()
			receivedTopics[env.Topic]++
			mu.Unlock()
			return nil
		})
	}

	// Publish only to SigningRequested
	for i := 0; i < 3; i++ {
		_ = bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if got := receivedTopics[domain.EventTypeSigningRequested]; got != 3 {
		t.Errorf("SigningRequested received %d, want 3", got)
	}
	if got := receivedTopics[domain.EventTypeSigningSucceeded]; got != 0 {
		t.Errorf("SigningSucceeded received %d, want 0 (topic isolation violated)", got)
	}
}

func TestMemoryBus_Close_DrainsPending(t *testing.T) {
	bus := eventbus.NewMemoryBus(eventbus.MemoryBusConfig{
		BufferSize:      16,
		WorkersPerTopic: 2,
		HandlerTimeout:  time.Second,
	})

	var processed atomic.Int64
	_, _ = bus.Subscribe(domain.EventTypeSigningRequested, func(_ context.Context, _ domain.Envelope) error {
		time.Sleep(5 * time.Millisecond) // simulate work
		processed.Add(1)
		return nil
	})

	// Publish a batch before closing
	for i := 0; i < 5; i++ {
		_ = bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))
	}

	// Close should block until in-flight events are processed
	if err := bus.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := processed.Load(); got != 5 {
		t.Errorf("processed %d events after Close, want 5", got)
	}
}

func TestMemoryBus_PublishAfterClose_ReturnsError(t *testing.T) {
	bus := eventbus.NewMemoryBus(eventbus.MemoryBusConfig{BufferSize: 8})
	_ = bus.Close()

	err := bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))
	if err == nil {
		t.Fatal("expected error after Close, got nil")
	}
	var closedErr eventbus.ErrBusClosed
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ErrBusClosed, got %T: %v", err, err)
	}
}

func TestMemoryBus_SubscribeAfterClose_ReturnsError(t *testing.T) {
	bus := eventbus.NewMemoryBus(eventbus.MemoryBusConfig{BufferSize: 8})
	_ = bus.Close()

	_, err := bus.Subscribe(domain.EventTypeSigningRequested, func(_ context.Context, _ domain.Envelope) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error after Close, got nil")
	}
}

func TestMemoryBus_ConcurrentPublishers(t *testing.T) {
	bus := newTestBus(t, eventbus.MemoryBusConfig{
		BufferSize:      256,
		WorkersPerTopic: 4,
	})

	const publishers = 10
	const eventsPerPublisher = 20
	var received atomic.Int64

	_, _ = bus.Subscribe(domain.EventTypeSigningRequested, func(_ context.Context, _ domain.Envelope) error {
		received.Add(1)
		return nil
	})

	var wg sync.WaitGroup
	wg.Add(publishers)
	for i := 0; i < publishers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < eventsPerPublisher; j++ {
				_ = bus.Publish(context.Background(), makeEnvelope(domain.EventTypeSigningRequested))
			}
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() == publishers*eventsPerPublisher {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("received %d events, want %d", received.Load(), publishers*eventsPerPublisher)
}
