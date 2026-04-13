// Package metadata defines the pluggable strongly-consistent metadata store
// interface used by the DAG, cache, registry, and mount-cache subsystems.
// Backends: etcd, TiKV, FoundationDB.
package metadata

import "context"

// KVPair is a key-value result from a scan operation.
type KVPair struct {
	Key   []byte
	Value []byte
	// ModRevision is the backend-specific modification version/revision
	// for optimistic concurrency (0 if not supported).
	ModRevision int64
}

// WatchEventType indicates what changed.
type WatchEventType string

const (
	WatchEventPut    WatchEventType = "PUT"
	WatchEventDelete WatchEventType = "DELETE"
)

// WatchEvent carries a change notification from the store.
type WatchEvent struct {
	Type        WatchEventType
	Key         []byte
	Value       []byte
	PrevValue   []byte
	ModRevision int64
}

// PutOption is a functional option for Put operations.
type PutOption func(*putOptions)

type putOptions struct {
	ttlSeconds int64
	prevRevision int64
	onlyIfAbsent bool
}

// WithTTL sets an expiry on the key (backend must support it).
func WithTTL(seconds int64) PutOption {
	return func(o *putOptions) { o.ttlSeconds = seconds }
}

// WithIfAbsent makes the Put a no-op if the key already exists.
func WithIfAbsent() PutOption {
	return func(o *putOptions) { o.onlyIfAbsent = true }
}

// WithPrevRevision makes the Put conditional on the current mod revision
// (optimistic concurrency control).
func WithPrevRevision(rev int64) PutOption {
	return func(o *putOptions) { o.prevRevision = rev }
}

// Txn is a transaction handle passed to Store.Txn.
type Txn interface {
	Get(key []byte) ([]byte, error)
	Put(key, value []byte, opts ...PutOption) error
	Delete(key []byte) error
	Scan(start, end []byte, limit int) ([]KVPair, error)
	ScanPrefix(prefix []byte, limit int) ([]KVPair, error)
}

// Store is the pluggable strongly-consistent key-value store interface.
// All keys and values are raw bytes; callers handle serialisation.
// All implementations must be safe for concurrent use.
type Store interface {
	// ── Single-key operations ─────────────────────────────────────────────

	// Get returns the value for key. Returns (nil, ErrKeyNotFound) if absent.
	Get(ctx context.Context, key []byte) ([]byte, error)

	// Put stores key→value. Honours functional options (TTL, CAS, etc.).
	Put(ctx context.Context, key, value []byte, opts ...PutOption) error

	// Delete removes key. Returns nil if already absent.
	Delete(ctx context.Context, key []byte) error

	// ── Range / prefix scans ──────────────────────────────────────────────

	// Scan returns at most limit KVPairs with start ≤ key < end.
	// Pass limit=0 for "all".
	Scan(ctx context.Context, start, end []byte, limit int) ([]KVPair, error)

	// ScanPrefix returns all keys that begin with prefix, up to limit.
	ScanPrefix(ctx context.Context, prefix []byte, limit int) ([]KVPair, error)

	// ── Atomic operations ─────────────────────────────────────────────────

	// CompareAndSwap atomically replaces oldValue with newValue.
	// Returns true on success.
	CompareAndSwap(ctx context.Context, key, oldValue, newValue []byte) (bool, error)

	// AtomicIncrement atomically adds delta to the int64 stored at key
	// (treating an absent key as 0) and returns the new value.
	AtomicIncrement(ctx context.Context, key []byte, delta int64) (int64, error)

	// ── Transactions ──────────────────────────────────────────────────────

	// Txn executes fn within a serialisable transaction.
	// fn may be retried on conflict; it must be idempotent.
	Txn(ctx context.Context, fn func(Txn) error) error

	// ── Watch ─────────────────────────────────────────────────────────────

	// Watch streams change events for key until ctx is cancelled.
	Watch(ctx context.Context, key []byte) (<-chan WatchEvent, error)

	// WatchPrefix streams change events for all keys with the given prefix.
	WatchPrefix(ctx context.Context, prefix []byte) (<-chan WatchEvent, error)

	// ── Lifecycle ─────────────────────────────────────────────────────────

	// Close releases the backend connection.
	Close() error
}

// ErrKeyNotFound is returned by Get when a key is absent.
var ErrKeyNotFound = errKeyNotFound("key not found")

type errKeyNotFound string

func (e errKeyNotFound) Error() string { return string(e) }
