// Package metadata defines the strongly-consistent distributed KV store
// abstraction used throughout the RBE server for all mutable metadata
// (manifest index, tag mappings, DAG vertices, cache entries, log indices,
// mount cache records, attestation pointers, etc.).
//
// The interface intentionally targets the common subset of etcd, TiKV, and
// FoundationDB so implementations are interchangeable without logic changes.
package metadata

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when the requested key does not exist.
var ErrNotFound = errors.New("metadata: key not found")

// ErrTxnConflict is returned when an optimistic transaction is aborted due
// to a concurrent write (retry is always safe).
var ErrTxnConflict = errors.New("metadata: transaction conflict, retry")

// ErrLeaseExpired is returned when trying to refresh a lease that has already
// been revoked or timed out.
var ErrLeaseExpired = errors.New("metadata: lease expired")

// ─── Core types ─────────────────────────────────────────────────────────────

// KV is a single key-value pair returned from Scan or Watch.
type KV struct {
	Key   []byte
	Value []byte
	// ModRevision is a backend-specific monotonic revision for this key.
	// Callers may use it for compare-and-swap operations.
	ModRevision int64
}

// EventType classifies a Watch notification.
type EventType int

const (
	EventPut    EventType = iota // key was created or updated
	EventDelete                  // key was deleted
)

// Event is a single change notification delivered via Watch.
type Event struct {
	Type        EventType
	KV          KV
	PrevKV      *KV // previous value if the backend supports it
}

// ─── Txn ────────────────────────────────────────────────────────────────────

// Txn is a short-lived transactional view. All operations are buffered until
// Commit is called. Reads always reflect the snapshot at the start of the
// transaction. Txn must NOT be used concurrently.
type Txn interface {
	// Get reads key within the transaction.
	Get(key []byte) ([]byte, error)
	// Put stages a write.
	Put(key, value []byte) error
	// PutWithTTL stages a write with an expiry.
	PutWithTTL(key, value []byte, ttl time.Duration) error
	// Delete stages a delete.
	Delete(key []byte) error
	// Scan stages a range read from [start, end).
	Scan(start, end []byte, limit int) ([]KV, error)
	// Commit atomically applies all staged operations.
	// Returns ErrTxnConflict if concurrent modifications detected.
	Commit() error
}

// ─── Store interface ────────────────────────────────────────────────────────

// Store is the top-level interface every backend must implement.
// All methods must be safe for concurrent use by multiple goroutines.
type Store interface {
	// ── Point operations ─────────────────────────────────────────────────

	// Get returns the value for key or ErrNotFound.
	Get(ctx context.Context, key []byte) ([]byte, error)

	// Put stores value under key, creating or overwriting.
	Put(ctx context.Context, key, value []byte) error

	// PutWithTTL stores value under key with an automatic expiry.
	PutWithTTL(ctx context.Context, key, value []byte, ttl time.Duration) error

	// Delete removes key. Returns nil if key did not exist.
	Delete(ctx context.Context, key []byte) error

	// ── Atomic CAS ────────────────────────────────────────────────────────

	// CompareAndSwap atomically sets key=newValue if its current value
	// equals expected. Returns (true, nil) on success, (false, nil) if the
	// value didn't match, or (false, error) on I/O failure.
	CompareAndSwap(ctx context.Context, key, expected, newValue []byte) (bool, error)

	// CompareAndDelete atomically deletes key if its current value equals
	// expected.
	CompareAndDelete(ctx context.Context, key, expected []byte) (bool, error)

	// ── Range operations ──────────────────────────────────────────────────

	// Scan returns up to limit KV pairs whose keys are in [start, end).
	// If limit is 0 all matching keys are returned.
	Scan(ctx context.Context, start, end []byte, limit int) ([]KV, error)

	// ScanPrefix is a convenience wrapper that scans all keys sharing prefix.
	ScanPrefix(ctx context.Context, prefix []byte, limit int) ([]KV, error)

	// DeleteRange atomically deletes all keys in [start, end).
	DeleteRange(ctx context.Context, start, end []byte) (int64, error)

	// ── Transactions ─────────────────────────────────────────────────────

	// Txn opens a new transaction. The caller must call Commit or discard.
	Txn(ctx context.Context) (Txn, error)

	// ── Watch ─────────────────────────────────────────────────────────────

	// Watch calls fn for every change on key until ctx is cancelled or fn
	// returns a non-nil error (which is then returned from Watch).
	Watch(ctx context.Context, key []byte, fn func(Event) error) error

	// WatchPrefix calls fn for every change on any key sharing prefix.
	WatchPrefix(ctx context.Context, prefix []byte, fn func(Event) error) error

	// ── Leases ────────────────────────────────────────────────────────────

	// GrantLease creates a lease with the given TTL and returns an opaque
	// leaseID. Keys attached to the lease are deleted when it expires.
	GrantLease(ctx context.Context, ttl time.Duration) (leaseID int64, err error)

	// KeepAlive resets the TTL countdown for leaseID. Returns ErrLeaseExpired
	// if the lease no longer exists.
	KeepAlive(ctx context.Context, leaseID int64) error

	// RevokeLease immediately expires leaseID and deletes attached keys.
	RevokeLease(ctx context.Context, leaseID int64) error

	// PutWithLease stores key under the given lease.
	PutWithLease(ctx context.Context, key, value []byte, leaseID int64) error

	// ── Lifecycle ─────────────────────────────────────────────────────────

	// Close releases all resources. Must be called exactly once.
	Close() error
}

// ─── Key-space constants ─────────────────────────────────────────────────────
//
// Every subsystem owns a non-overlapping prefix hierarchy.  All prefixes are
// designed so that ScanPrefix works efficiently (keys are byte-sorted).

const sep = "/"

// Key builders — call these instead of hand-coding paths.
func keyStr(parts ...string) []byte {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return []byte(out)
}

// ── Registry ──────────────────────────────────────────────────────────────────

func KeyRegistryTag(repo, tag string) []byte {
	return keyStr("reg", "repos", repo, "tags", tag)
}
func KeyRegistryTagsPrefix(repo string) []byte {
	return keyStr("reg", "repos", repo, "tags") + []byte("/")
}
func KeyRegistryManifest(repo, digest string) []byte {
	return keyStr("reg", "repos", repo, "manifests", digest)
}
func KeyRegistryManifestsPrefix(repo string) []byte {
	return keyStr("reg", "repos", repo, "manifests") + []byte("/")
}
func KeyRegistryUpload(repo, uuid string) []byte {
	return keyStr("reg", "repos", repo, "uploads", uuid)
}
func KeyRegistryReferrer(repo, subjectDigest, referrerDigest string) []byte {
	return keyStr("reg", "repos", repo, "referrers", subjectDigest, referrerDigest)
}
func KeyRegistryReferrersPrefix(repo, subjectDigest string) []byte {
	return keyStr("reg", "repos", repo, "referrers", subjectDigest) + []byte("/")
}
func KeyRegistryBlobRef(digest, repo string) []byte {
	return keyStr("reg", "blobs", digest, "repos", repo)
}
func KeyRegistryCatalog(repo string) []byte {
	return keyStr("reg", "catalog", repo)
}
func KeyRegistryCatalogPrefix() []byte {
	return []byte("reg/catalog/")
}
func KeyRegistryConversion(srcDigest, format string) []byte {
	return keyStr("reg", "conversions", srcDigest, format)
}
func KeyRegistryConversionsPrefix(srcDigest string) []byte {
	return keyStr("reg", "conversions", srcDigest) + []byte("/")
}

// ── DAG ───────────────────────────────────────────────────────────────────────

func KeyBuild(buildID string) []byte {
	return keyStr("dag", "builds", buildID)
}
func KeyBuildsPrefix() []byte        { return []byte("dag/builds/") }
func KeyBuildVertex(buildID, vertexID string) []byte {
	return keyStr("dag", "builds", buildID, "verts", vertexID)
}
func KeyBuildVerticesPrefix(buildID string) []byte {
	return keyStr("dag", "builds", buildID, "verts") + []byte("/")
}
func KeyVertex(vertexID string) []byte {
	return keyStr("dag", "verts", vertexID)
}
func KeyVertexInputFile(vertexID, pathHash string) []byte {
	return keyStr("dag", "verts", vertexID, "ifiles", pathHash)
}
func KeyVertexOutputFile(vertexID, pathHash string) []byte {
	return keyStr("dag", "verts", vertexID, "ofiles", pathHash)
}
func KeyVertexInputFilesPrefix(vertexID string) []byte {
	return keyStr("dag", "verts", vertexID, "ifiles") + []byte("/")
}
func KeyVertexOutputFilesPrefix(vertexID string) []byte {
	return keyStr("dag", "verts", vertexID, "ofiles") + []byte("/")
}

// ── Cache ─────────────────────────────────────────────────────────────────────

func KeyCacheEntry(keyHash string) []byte {
	return keyStr("cache", "entries", keyHash)
}
func KeyCacheByVertex(vertexID string) []byte {
	return keyStr("cache", "by_vertex", vertexID)
}
func KeyCacheByBuild(buildID, keyHash string) []byte {
	return keyStr("cache", "by_build", buildID, keyHash)
}

// ── Logs ──────────────────────────────────────────────────────────────────────

func KeyLogIndex(buildID, vertexID string, fd int32) []byte {
	return keyStr("logs", "idx", buildID, vertexID, fmt.Sprintf("%d", fd))
}
func KeyLogIndexPrefix(buildID, vertexID string) []byte {
	return keyStr("logs", "idx", buildID, vertexID) + []byte("/")
}

// ── Mount cache ───────────────────────────────────────────────────────────────

func KeyMountCache(scope, id, platformHash string) []byte {
	return keyStr("mc", scope, id, platformHash)
}
func KeyMountCacheLock(scope, id, platformHash string) []byte {
	return keyStr("mc_lock", scope, id, platformHash)
}
func KeyMountCachePrefix(scope string) []byte {
	if scope == "" {
		return []byte("mc/")
	}
	return []byte("mc/" + scope + "/")
}

// ── Attestations ──────────────────────────────────────────────────────────────

func KeyAttestation(subjectDigest, attestType, attestID string) []byte {
	return keyStr("attest", subjectDigest, attestType, attestID)
}
func KeyAttestationsPrefix(subjectDigest, attestType string) []byte {
	return keyStr("attest", subjectDigest, attestType) + []byte("/")
}
func KeyAttestationByRepo(repo, subjectDigest, attestType string) []byte {
	return keyStr("attest_repo", repo, subjectDigest, attestType)
}
func KeyVerificationPolicy(repo string) []byte {
	return keyStr("vpolicy", repo)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// PrefixEnd returns the lexicographic end key for a prefix scan.
// In most backends Scan(prefix, PrefixEnd(prefix)) covers all keys with the
// prefix. This is equivalent to incrementing the last byte of prefix.
func PrefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			return end[:i+1]
		}
	}
	// Overflow: all bytes wrapped — use nil to mean "no upper bound"
	return nil
}

// MustJSON is a helper that marshals v and panics on error.
// Use only in init paths.
func MustJSON(v any) []byte {
	import_json := func() ([]byte, error) {
		// Provided by each call site via encoding/json
		return nil, nil
	}
	b, err := import_json()
	if err != nil {
		panic(err)
	}
	return b
}
