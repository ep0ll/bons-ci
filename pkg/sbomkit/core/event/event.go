// Package event provides a typed, topic-based domain event system for
// decoupled communication between pipeline stages. All types are value-safe
// and safe for concurrent use.
package event

import (
	"context"
	"time"
)

// Topic is an opaque event channel identifier.
type Topic string

// Lifecycle topics emitted by the engine and pipeline.
const (
	// Scan lifecycle
	TopicScanRequested Topic = "sbom.scan.requested"
	TopicScanStarted   Topic = "sbom.scan.started"
	TopicScanProgress  Topic = "sbom.scan.progress"
	TopicScanCompleted Topic = "sbom.scan.completed"
	TopicScanFailed    Topic = "sbom.scan.failed"

	// Resolve lifecycle
	TopicResolveStarted   Topic = "sbom.resolve.started"
	TopicResolveCompleted Topic = "sbom.resolve.completed"
	TopicResolveFailed    Topic = "sbom.resolve.failed"

	// Export lifecycle
	TopicExportStarted   Topic = "sbom.export.started"
	TopicExportCompleted Topic = "sbom.export.completed"
	TopicExportFailed    Topic = "sbom.export.failed"

	// Cache signals
	TopicCacheHit     Topic = "sbom.cache.hit"
	TopicCacheMiss    Topic = "sbom.cache.miss"
	TopicCacheEvicted Topic = "sbom.cache.evicted"
)

// Event is the immutable envelope for all domain events.
// The zero value is not useful; construct via Bus.Publish or Bus.PublishAsync.
type Event struct {
	// ID is a unique identifier for this specific event instance.
	ID string
	// CorrelationID groups all events belonging to a single scan operation.
	CorrelationID string
	// Topic identifies the event category.
	Topic Topic
	// Timestamp records when the event was created (UTC).
	Timestamp time.Time
	// Payload holds the typed data for this topic; use the Payload* types below.
	Payload any

	// ctx is the context propagated from the originating call. Private to
	// prevent mutation after construction.
	ctx context.Context
}

// Context returns the context propagated into this event.
// Falls back to context.Background() when none was set.
func (e Event) Context() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

// Handler processes an incoming event. A non-nil error is logged by the bus
// but delivery continues to remaining subscribers.
type Handler func(Event) error

// Predicate is an optional filter evaluated before invoking a Handler.
// A nil Predicate means "accept all events on this topic".
type Predicate func(Event) bool

// ── Typed payload types ──────────────────────────────────────────────────────
// Each topic has a corresponding payload struct. Callers should type-assert
// event.Payload to the appropriate type.

// ScanRequestedPayload is emitted on TopicScanRequested.
type ScanRequestedPayload struct {
	RequestID  string
	Source     string
	SourceKind string
	Format     string
}

// ScanProgressPayload is emitted on TopicScanStarted and TopicScanProgress.
type ScanProgressPayload struct {
	RequestID string
	Stage     string  // "resolving" | "scanning" | "exporting"
	Percent   float64 // 0–100
	Message   string
}

// ScanCompletedPayload is emitted on TopicScanCompleted.
type ScanCompletedPayload struct {
	RequestID      string
	ComponentCount int
	Format         string
	DurationMs     int64
	CacheHit       bool
}

// ScanFailedPayload is emitted on TopicScanFailed.
type ScanFailedPayload struct {
	RequestID string
	Stage     string
	Err       error
}

// ResolveCompletedPayload is emitted on TopicResolveCompleted.
type ResolveCompletedPayload struct {
	RequestID        string
	ResolvedIdentity string // e.g. image digest
}

// ExportCompletedPayload is emitted on TopicExportCompleted.
type ExportCompletedPayload struct {
	RequestID string
	Format    string
	ByteCount int
}

// CacheHitPayload is emitted on TopicCacheHit.
type CacheHitPayload struct {
	RequestID string
	CacheKey  string
}

// CacheMissPayload is emitted on TopicCacheMiss.
type CacheMissPayload struct {
	RequestID string
	CacheKey  string
}
