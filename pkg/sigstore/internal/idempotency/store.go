// Package idempotency provides the IdempotencyStore interface and an in-memory
// implementation backed by a concurrent map with TTL expiry.
//
// Why idempotency at this layer?
//   - Event buses can redeliver on crash recovery or network retries.
//   - Signing the same image twice with different ephemeral keys creates
//     duplicate Rekor entries and confuses policy evaluation.
//   - The store is the single gate that prevents duplicate work regardless
//     of which layer triggers the retry.
//
// Production note: replace MemoryIdempotencyStore with a Redis-backed
// implementation for multi-replica deployments. The interface contract
// is identical — only the constructor changes at bootstrap.
package idempotency

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Status represents the outcome recorded in the store.
type Status string

const (
	StatusPending   Status = "pending" // claimed but not yet complete
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Record is what the store persists for each idempotency key.
type Record struct {
	Key       string
	Status    Status
	CreatedAt time.Time
	ExpiresAt time.Time
	// ResultRef is an opaque payload (e.g. SignatureRef) stored on success
	// so callers can return the same result without re-signing.
	ResultRef string
}

// IdempotencyStore is the interface every idempotency backend must implement.
//
// ISP note: the interface has three methods that map cleanly to three distinct
// call sites: claim before work, succeed/fail after work, and check on receipt.
type IdempotencyStore interface {
	// TryClaim attempts to claim key for processing.
	// Returns (true, nil) if the claim succeeded.
	// Returns (false, nil) if already claimed (duplicate request).
	// Returns (false, err) on store failure.
	TryClaim(ctx context.Context, key string, ttl time.Duration) (claimed bool, err error)

	// MarkSucceeded records a successful outcome and stores resultRef.
	// Must be called after successful processing to release the pending state.
	MarkSucceeded(ctx context.Context, key, resultRef string) error

	// MarkFailed records a failed outcome.
	// A failed record can be re-claimed after its TTL expires (retry window).
	MarkFailed(ctx context.Context, key string, reason string) error

	// Get returns the current record for key, or ErrKeyNotFound.
	Get(ctx context.Context, key string) (Record, error)
}

// ErrKeyNotFound is returned by Get when the key has no record (or has expired).
type ErrKeyNotFound struct{ Key string }

func (e *ErrKeyNotFound) Error() string { return "idempotency key not found: " + e.Key }

// --- MemoryIdempotencyStore ─────────────────────────────────────────────────

type entry struct {
	record Record
}

// MemoryIdempotencyStore is a concurrency-safe, TTL-expiring in-memory store.
//
// TTL expiry is evaluated lazily on Get/TryClaim rather than with a background
// goroutine, to avoid goroutine leaks and keep the implementation single-phase.
//
// PRODUCTION replacement: implement IdempotencyStore over Redis with
// SET key NX PX ttl for atomic TryClaim semantics. Multi-replica safety
// requires a distributed lock — Redis SETNX is the canonical solution.
type MemoryIdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]*entry
}

// NewMemoryIdempotencyStore returns an empty store.
func NewMemoryIdempotencyStore() *MemoryIdempotencyStore {
	return &MemoryIdempotencyStore{entries: make(map[string]*entry)}
}

// TryClaim atomically claims key for the caller.
// Uses a mutex rather than a lock-free map because correctness > throughput
// for idempotency gates (the signing path itself is the bottleneck).
func (s *MemoryIdempotencyStore) TryClaim(_ context.Context, key string, ttl time.Duration) (bool, error) {
	if key == "" {
		return false, fmt.Errorf("idempotency: key must not be empty")
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	e, exists := s.entries[key]
	if exists && now.Before(e.record.ExpiresAt) {
		// Active record exists — duplicate or in-progress.
		return false, nil
	}

	// Either key doesn't exist or the previous record expired.
	s.entries[key] = &entry{
		record: Record{
			Key:       key,
			Status:    StatusPending,
			CreatedAt: now,
			ExpiresAt: now.Add(ttl),
		},
	}
	return true, nil
}

func (s *MemoryIdempotencyStore) MarkSucceeded(_ context.Context, key, resultRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[key]
	if !ok {
		return &ErrKeyNotFound{Key: key}
	}
	e.record.Status = StatusSucceeded
	e.record.ResultRef = resultRef
	return nil
}

func (s *MemoryIdempotencyStore) MarkFailed(_ context.Context, key string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[key]
	if !ok {
		return &ErrKeyNotFound{Key: key}
	}
	e.record.Status = StatusFailed
	return nil
}

func (s *MemoryIdempotencyStore) Get(_ context.Context, key string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[key]
	if !ok || time.Now().After(e.record.ExpiresAt) {
		return Record{}, &ErrKeyNotFound{Key: key}
	}
	return e.record, nil
}
