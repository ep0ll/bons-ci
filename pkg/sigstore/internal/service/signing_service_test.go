package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
	"github.com/bons/bons-ci/pkg/sigstore/internal/eventbus"
	"github.com/bons/bons-ci/pkg/sigstore/internal/idempotency"
	"github.com/bons/bons-ci/pkg/sigstore/internal/observability"
	"github.com/bons/bons-ci/pkg/sigstore/internal/resilience"
	"github.com/bons/bons-ci/pkg/sigstore/internal/service"
	"github.com/bons/bons-ci/pkg/sigstore/internal/signing"
	"github.com/prometheus/client_golang/prometheus"
)

// ══════════════════════════════════════════════════════════════════════════════
// Mock implementations
// ══════════════════════════════════════════════════════════════════════════════

// mockSigner records Sign calls and returns configurable results.
// Thread-safe via mutex so it can be inspected from test goroutines.
type mockSigner struct {
	mu      sync.Mutex
	calls   []signing.SignRequest
	result  domain.SigningResult
	errFunc func(attempt int) error // nil → always succeed
	attempt int
}

func (m *mockSigner) Sign(_ context.Context, req signing.SignRequest) (domain.SigningResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	m.attempt++
	if m.errFunc != nil {
		if err := m.errFunc(m.attempt); err != nil {
			return domain.SigningResult{}, err
		}
	}
	return m.result, nil
}

func (m *mockSigner) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// capturebus records all published events for assertion.
type captureBus struct {
	mu       sync.Mutex
	events   []domain.Envelope
	delegate eventbus.EventBus
}

func newCaptureBus(delegate eventbus.EventBus) *captureBus {
	return &captureBus{delegate: delegate}
}

func (b *captureBus) Publish(ctx context.Context, env domain.Envelope) error {
	b.mu.Lock()
	b.events = append(b.events, env)
	b.mu.Unlock()
	return b.delegate.Publish(ctx, env)
}

func (b *captureBus) Subscribe(topic domain.EventType, h eventbus.Handler) (eventbus.Subscription, error) {
	return b.delegate.Subscribe(topic, h)
}

func (b *captureBus) Close() error { return b.delegate.Close() }

func (b *captureBus) EventsOfType(t domain.EventType) []domain.Envelope {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []domain.Envelope
	for _, e := range b.events {
		if e.Topic == t {
			out = append(out, e)
		}
	}
	return out
}

// noopResiliencePolicy calls fn once with no retry or circuit breaking.
// Simplifies service tests that are not testing resilience behaviour.
type noopResiliencePolicy struct{}

func (p *noopResiliencePolicy) Execute(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// retryResiliencePolicy retries exactly maxAttempts times.
type retryResiliencePolicy struct{ maxAttempts int }

func (p *retryResiliencePolicy) Execute(ctx context.Context, fn func(context.Context) error) error {
	var last error
	for i := 0; i < p.maxAttempts; i++ {
		if last = fn(ctx); last == nil {
			return nil
		}
	}
	return fmt.Errorf("exhausted %d attempts: %w", p.maxAttempts, last)
}

// ══════════════════════════════════════════════════════════════════════════════
// Test helpers
// ══════════════════════════════════════════════════════════════════════════════

func newTestMetrics() *observability.Metrics {
	return observability.NewMetrics(prometheus.NewRegistry())
}

func newTestBus(t *testing.T) *eventbus.MemoryBus {
	t.Helper()
	bus := eventbus.NewMemoryBus(eventbus.MemoryBusConfig{
		BufferSize:      64,
		WorkersPerTopic: 2,
		HandlerTimeout:  5 * time.Second,
	})
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func newTestService(
	t *testing.T,
	bus eventbus.EventBus,
	signer signing.Signer,
	policy resilience.ResiliencePolicy,
	store idempotency.IdempotencyStore,
) *service.SigningService {
	t.Helper()
	svc, err := service.NewSigningService(service.Config{
		Bus:              bus,
		Signer:           signer,
		ResiliencePolicy: policy,
		IdempotencyStore: store,
		Metrics:          newTestMetrics(),
		IdempotencyTTL:   1 * time.Minute,
	})
	if err != nil {
		t.Fatalf("newTestService: %v", err)
	}
	if err = svc.RegisterHandlers(); err != nil {
		t.Fatalf("RegisterHandlers: %v", err)
	}
	return svc
}

func publishRequest(t *testing.T, ctx context.Context, bus eventbus.Publisher, imageRef, keyHint, corrID string) domain.SigningRequestedEvent {
	t.Helper()
	evt := domain.SigningRequestedEvent{
		BaseEvent: domain.BaseEvent{
			ID:            fmt.Sprintf("test-evt-%d", time.Now().UnixNano()),
			CorrelationID: corrID,
			OccurredAt:    time.Now().UTC(),
			Type:          domain.EventTypeSigningRequested,
			Version:       1,
		},
		ImageRef: imageRef,
		KeyHint:  keyHint,
	}
	if err := bus.Publish(ctx, domain.Envelope{
		Topic:   domain.EventTypeSigningRequested,
		Payload: evt,
	}); err != nil {
		t.Fatalf("publish request: %v", err)
	}
	return evt
}

func waitForEvents(t *testing.T, capture *captureBus, topic domain.EventType, wantCount int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(capture.EventsOfType(topic)) >= wantCount {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("timeout waiting for %d events of type %q (got %d)",
		wantCount, topic, len(capture.EventsOfType(topic)))
}

// ══════════════════════════════════════════════════════════════════════════════
// SigningService tests
// ══════════════════════════════════════════════════════════════════════════════

func TestSigningService_HappyPath(t *testing.T) {
	tests := []struct {
		name       string
		imageRef   string
		keyHint    string
		wantSigRef string
	}{
		{
			name:       "keyless signing produces succeeded event",
			imageRef:   "registry.example.com/app@sha256:abc123",
			keyHint:    "",
			wantSigRef: "registry.example.com/app@sha256:abc123.sig",
		},
		{
			name:       "static key signing with key hint",
			imageRef:   "registry.example.com/api@sha256:def456",
			keyHint:    "release-key",
			wantSigRef: "registry.example.com/api@sha256:def456.sig",
		},
		{
			name:       "image ref with tag and digest",
			imageRef:   "registry.example.com/app:v1.2.3@sha256:aaa000",
			keyHint:    "",
			wantSigRef: "registry.example.com/app:v1.2.3@sha256:aaa000.sig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			innerBus := newTestBus(t)
			capBus := newCaptureBus(innerBus)
			signer := &mockSigner{result: domain.SigningResult{
				ImageRef:      tt.imageRef,
				SignatureRef:  tt.wantSigRef,
				RekorLogIndex: 42,
				SignedAt:      time.Now(),
			}}
			store := idempotency.NewMemoryIdempotencyStore()
			_ = newTestService(t, capBus, signer, &noopResiliencePolicy{}, store)

			ctx := context.Background()
			publishRequest(t, ctx, capBus, tt.imageRef, tt.keyHint, "corr-001")

			waitForEvents(t, capBus, domain.EventTypeSigningSucceeded, 1, 3*time.Second)

			successes := capBus.EventsOfType(domain.EventTypeSigningSucceeded)
			if len(successes) != 1 {
				t.Fatalf("expected 1 succeeded event, got %d", len(successes))
			}

			succeeded, ok := successes[0].Payload.(domain.SigningSucceededEvent)
			if !ok {
				t.Fatalf("payload type = %T, want SigningSucceededEvent", successes[0].Payload)
			}
			if succeeded.SignatureRef != tt.wantSigRef {
				t.Errorf("signature_ref = %q, want %q", succeeded.SignatureRef, tt.wantSigRef)
			}
			if signer.CallCount() != 1 {
				t.Errorf("signer.Sign called %d times, want 1", signer.CallCount())
			}
		})
	}
}

func TestSigningService_SignerError_PublishesFailedEvent(t *testing.T) {
	tests := []struct {
		name      string
		signerErr error
		wantRetry bool
	}{
		{
			name:      "rekor unreachable → retryable failure",
			signerErr: &signing.ErrRekorUnreachable{Cause: errors.New("timeout")},
			wantRetry: true,
		},
		{
			name:      "context cancelled → non-retryable",
			signerErr: context.Canceled,
			wantRetry: false,
		},
		{
			name:      "generic signing error",
			signerErr: &signing.ErrSigningFailed{ImageRef: "img", Cause: errors.New("cert expired")},
			wantRetry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			innerBus := newTestBus(t)
			capBus := newCaptureBus(innerBus)
			signer := &mockSigner{errFunc: func(_ int) error { return tt.signerErr }}
			store := idempotency.NewMemoryIdempotencyStore()
			_ = newTestService(t, capBus, signer, &noopResiliencePolicy{}, store)

			publishRequest(t, context.Background(), capBus, "reg/img@sha256:000", "", "corr-002")

			waitForEvents(t, capBus, domain.EventTypeSigningFailed, 1, 3*time.Second)

			failures := capBus.EventsOfType(domain.EventTypeSigningFailed)
			if len(failures) == 0 {
				t.Fatal("expected at least one signing.failed event")
			}
			evt, ok := failures[0].Payload.(domain.SigningFailedEvent)
			if !ok {
				t.Fatalf("unexpected payload type %T", failures[0].Payload)
			}
			if evt.Retryable != tt.wantRetry {
				t.Errorf("retryable = %v, want %v", evt.Retryable, tt.wantRetry)
			}
		})
	}
}

func TestSigningService_Idempotency_DuplicateRequest(t *testing.T) {
	innerBus := newTestBus(t)
	capBus := newCaptureBus(innerBus)
	signer := &mockSigner{result: domain.SigningResult{
		ImageRef:     "reg/img@sha256:dup",
		SignatureRef: "reg/img@sha256:dup.sig",
	}}
	store := idempotency.NewMemoryIdempotencyStore()
	_ = newTestService(t, capBus, signer, &noopResiliencePolicy{}, store)

	ctx := context.Background()
	// Same eventID published twice simulates network-level redelivery.
	evt := publishRequest(t, ctx, capBus, "reg/img@sha256:dup", "", "corr-003")

	// Re-publish the exact same event (same ID → same idempotency key).
	_ = capBus.Publish(ctx, domain.Envelope{
		Topic:   domain.EventTypeSigningRequested,
		Payload: evt, // same object, same ID
	})

	// Wait for both to be processed.
	time.Sleep(200 * time.Millisecond)

	// Signer should have been called exactly once.
	if got := signer.CallCount(); got != 1 {
		t.Errorf("signer called %d times, want exactly 1 (idempotency violation)", got)
	}

	// One succeeded + one duplicate event expected.
	dups := capBus.EventsOfType(domain.EventTypeSigningDuplicate)
	if len(dups) == 0 {
		t.Error("expected at least one signing.duplicate event")
	}
}

func TestSigningService_RetryOnTransientError(t *testing.T) {
	// Signer fails on first two calls, succeeds on third.
	failTimes := 2
	signer := &mockSigner{
		errFunc: func(attempt int) error {
			if attempt <= failTimes {
				return errors.New("transient error")
			}
			return nil
		},
		result: domain.SigningResult{
			ImageRef:     "reg/img@sha256:retry",
			SignatureRef: "reg/img@sha256:retry.sig",
		},
	}

	innerBus := newTestBus(t)
	capBus := newCaptureBus(innerBus)
	store := idempotency.NewMemoryIdempotencyStore()
	policy := &retryResiliencePolicy{maxAttempts: 3}

	_ = newTestService(t, capBus, signer, policy, store)
	publishRequest(t, context.Background(), capBus, "reg/img@sha256:retry", "", "corr-004")

	waitForEvents(t, capBus, domain.EventTypeSigningSucceeded, 1, 5*time.Second)

	if got := signer.CallCount(); got != 3 {
		t.Errorf("signer called %d times, want 3 (2 failures + 1 success)", got)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// NewSigningService construction tests
// ══════════════════════════════════════════════════════════════════════════════

func TestNewSigningService_MissingDependencies(t *testing.T) {
	bus := newTestBus(t)
	signer := &mockSigner{}
	policy := &noopResiliencePolicy{}
	store := idempotency.NewMemoryIdempotencyStore()

	tests := []struct {
		name    string
		cfg     service.Config
		wantErr string
	}{
		{
			name:    "nil bus",
			cfg:     service.Config{Signer: signer, ResiliencePolicy: policy, IdempotencyStore: store},
			wantErr: "Bus is required",
		},
		{
			name:    "nil signer",
			cfg:     service.Config{Bus: bus, ResiliencePolicy: policy, IdempotencyStore: store},
			wantErr: "Signer is required",
		},
		{
			name:    "nil resilience policy",
			cfg:     service.Config{Bus: bus, Signer: signer, IdempotencyStore: store},
			wantErr: "ResiliencePolicy is required",
		},
		{
			name:    "nil idempotency store",
			cfg:     service.Config{Bus: bus, Signer: signer, ResiliencePolicy: policy},
			wantErr: "IdempotencyStore is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.NewSigningService(tt.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !containsStr(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
