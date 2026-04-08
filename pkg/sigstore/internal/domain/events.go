// Package domain defines immutable domain events and value objects for the
// signing service. Events are the single source of truth for all state
// transitions — no mutability, no shared state.
//
// Architectural decision: Events are plain structs (not interfaces) so they
// are trivially serialisable, diffable, and comparable. Every field is
// exported for JSON/YAML marshalling without reflection tricks.
package domain

import (
	"time"
)

// EventType is a typed string so the compiler rejects raw string comparisons.
type EventType string

const (
	EventTypeSigningRequested  EventType = "signing.requested"
	EventTypeSigningStarted    EventType = "signing.started"
	EventTypeSigningSucceeded  EventType = "signing.succeeded"
	EventTypeSigningFailed     EventType = "signing.failed"
	EventTypeSigningDuplicate  EventType = "signing.duplicate"  // idempotency guard
	EventTypeRekorEntryCreated EventType = "rekor.entry.created"
	EventTypeDeadLetter        EventType = "signing.dead_letter"
)

// BaseEvent carries fields common to every event. Embed this in all events.
// OccurredAt is set at construction and never mutated.
type BaseEvent struct {
	ID          string    `json:"id"`           // UUID v4, globally unique
	CorrelationID string  `json:"correlation_id"` // traces a request end-to-end
	OccurredAt  time.Time `json:"occurred_at"`
	Type        EventType `json:"type"`
	Version     int       `json:"version"` // schema version for forward-compat
}

// --- concrete events --------------------------------------------------------

// SigningRequestedEvent is the entry-point event published by API handlers.
// It is the only event that carries raw user input; all downstream events
// derive their payload from this one.
type SigningRequestedEvent struct {
	BaseEvent
	ImageRef   string            `json:"image_ref"`   // e.g. registry/repo:tag@digest
	KeyHint    string            `json:"key_hint"`    // empty → keyless
	Annotations map[string]string `json:"annotations"` // passed through to cosign
}

// SigningStartedEvent is emitted when a worker claims the request.
// Publishing this before doing work enables at-most-once semantics when
// combined with the IdempotencyStore.
type SigningStartedEvent struct {
	BaseEvent
	ImageRef string `json:"image_ref"`
	WorkerID string `json:"worker_id"`
}

// SigningSucceededEvent carries the immutable attestation result.
type SigningSucceededEvent struct {
	BaseEvent
	ImageRef      string `json:"image_ref"`
	SignatureRef  string `json:"signature_ref"`  // OCI ref where sig is pushed
	RekorLogIndex int64  `json:"rekor_log_index"` // 0 if transparency log skipped
	CertChain     string `json:"cert_chain"`      // PEM; empty for static-key flows
	DurationMs    int64  `json:"duration_ms"`
}

// SigningFailedEvent carries structured error information for observability
// and dead-letter routing.
type SigningFailedEvent struct {
	BaseEvent
	ImageRef    string `json:"image_ref"`
	Reason      string `json:"reason"`      // human-readable
	Retryable   bool   `json:"retryable"`   // false → route to dead-letter
	AttemptNum  int    `json:"attempt_num"`
}

// SigningDuplicateEvent is emitted when the idempotency store rejects a
// duplicate request. Consumers can safely ignore this or update dashboards.
type SigningDuplicateEvent struct {
	BaseEvent
	ImageRef        string `json:"image_ref"`
	OriginalEventID string `json:"original_event_id"`
}

// RekorEntryCreatedEvent confirms the Rekor log entry — published after the
// Rekor client confirms the entry was accepted.
type RekorEntryCreatedEvent struct {
	BaseEvent
	ImageRef  string `json:"image_ref"`
	LogIndex  int64  `json:"log_index"`
	EntryHash string `json:"entry_hash"`
}

// DeadLetterEvent wraps any event that exhausted all retry attempts.
// The RawPayload preserves the original event for manual inspection or replay.
type DeadLetterEvent struct {
	BaseEvent
	OriginalType EventType   `json:"original_type"`
	OriginalID   string      `json:"original_id"`
	Reason       string      `json:"reason"`
	AttemptCount int         `json:"attempt_count"`
	RawPayload   interface{} `json:"raw_payload"`
}

// Envelope is the wire format published onto the EventBus. Wrapping events in
// an envelope keeps routing metadata out of domain types and makes
// serialisation / versioning a transport concern.
type Envelope struct {
	// Topic determines routing; conventionally mirrors EventType.
	Topic   EventType   `json:"topic"`
	Payload interface{} `json:"payload"`
}
