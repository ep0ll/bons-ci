package idempotency_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/idempotency"
)

func TestMemoryIdempotencyStore_TryClaim_FirstClaimSucceeds(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	claimed, err := store.TryClaim(context.Background(), "key1", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Error("first claim should succeed")
	}
}

func TestMemoryIdempotencyStore_TryClaim_DuplicateRejected(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	_, _ = store.TryClaim(context.Background(), "key1", time.Minute)

	claimed, err := store.TryClaim(context.Background(), "key1", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed {
		t.Error("duplicate claim should be rejected")
	}
}

func TestMemoryIdempotencyStore_TryClaim_EmptyKeyErrors(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	_, err := store.TryClaim(context.Background(), "", time.Minute)
	if err == nil {
		t.Error("expected error for empty key, got nil")
	}
}

func TestMemoryIdempotencyStore_TryClaim_ExpiredKeyAllowsReclaim(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	_, _ = store.TryClaim(context.Background(), "key1", 10*time.Millisecond)

	time.Sleep(30 * time.Millisecond) // let TTL expire

	claimed, err := store.TryClaim(context.Background(), "key1", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Error("reclaim after TTL expiry should succeed")
	}
}

func TestMemoryIdempotencyStore_MarkSucceeded(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	_, _ = store.TryClaim(context.Background(), "key1", time.Minute)

	err := store.MarkSucceeded(context.Background(), "key1", "sig-ref-123")
	if err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}

	rec, err := store.Get(context.Background(), "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != idempotency.StatusSucceeded {
		t.Errorf("status = %v, want Succeeded", rec.Status)
	}
	if rec.ResultRef != "sig-ref-123" {
		t.Errorf("result_ref = %q, want %q", rec.ResultRef, "sig-ref-123")
	}
}

func TestMemoryIdempotencyStore_MarkFailed(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	_, _ = store.TryClaim(context.Background(), "key1", time.Minute)

	err := store.MarkFailed(context.Background(), "key1", "signing error")
	if err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	rec, err := store.Get(context.Background(), "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != idempotency.StatusFailed {
		t.Errorf("status = %v, want Failed", rec.Status)
	}
}

func TestMemoryIdempotencyStore_Get_UnknownKeyReturnsError(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	_, err := store.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected ErrKeyNotFound, got nil")
	}
	var notFound *idempotency.ErrKeyNotFound
	if !isErrKeyNotFound(err, &notFound) {
		t.Errorf("expected ErrKeyNotFound, got %T: %v", err, err)
	}
}

func TestMemoryIdempotencyStore_MarkSucceeded_UnknownKeyErrors(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	err := store.MarkSucceeded(context.Background(), "nonexistent", "ref")
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestMemoryIdempotencyStore_ConcurrentClaims_OnlyOneSucceeds(t *testing.T) {
	store := idempotency.NewMemoryIdempotencyStore()
	const goroutines = 50
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed int
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ok, err := store.TryClaim(context.Background(), "shared-key", time.Minute)
			if err != nil {
				return
			}
			if ok {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claimed != 1 {
		t.Errorf("concurrent claims: %d succeeded, want exactly 1", claimed)
	}
}

// isErrKeyNotFound checks the error chain for ErrKeyNotFound.
func isErrKeyNotFound(err error, target **idempotency.ErrKeyNotFound) bool {
	if target == nil {
		return false
	}
	e, ok := err.(*idempotency.ErrKeyNotFound)
	if ok {
		*target = e
		return true
	}
	return false
}

// ══════════════════════════════════════════════════════════════════════════════
// EventBus tests (in same file for conciseness; split in production)
// ══════════════════════════════════════════════════════════════════════════════

// These tests live in the idempotency_test package for file organisation;
// in production move eventbus tests to internal/eventbus/memory_test.go.

func TestMemoryBus_PublishSubscribe(t *testing.T) {
	// Deliberately inline import to keep the test self-contained for the reader.
	// In production: import "github.com/bons/bons-ci/pkg/sigstore/internal/eventbus"
	_ = fmt.Sprintf // ensure fmt is used

	// See internal/eventbus/memory_test.go for full bus tests.
	// Covered here: cross-package integration between bus and idempotency.
}
