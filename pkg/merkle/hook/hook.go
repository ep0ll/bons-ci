// Package hook provides the Hook interface and HookChain for observing and
// intercepting the deduplication pipeline lifecycle.
//
// Hooks are the primary extension point. They enable:
//   - Structured logging of every cache hit/miss
//   - Prometheus/OpenTelemetry metrics emission
//   - Distributed tracing span creation
//   - Debug tooling (printing Merkle trees, dumping cache state)
//   - Policy enforcement (reject events from unknown layers)
//   - Testing (recording all events for assertion)
//
// # Hook execution
//
// Hooks in a HookChain are executed in registration order. If a hook returns
// a non-nil error:
//   - In strict mode (default): the error is propagated and the event is failed.
//   - In lenient mode: the error is recorded in HookEvent.Err but processing
//     continues with the remaining hooks.
//
// Hooks must not block indefinitely. Use the provided context for cancellation.
// Heavy work (I/O, network) should be done asynchronously via goroutines.
package hook

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// HookType
// ─────────────────────────────────────────────────────────────────────────────

// HookType identifies which pipeline lifecycle stage a HookEvent represents.
type HookType uint8

const (
	// HookEventReceived fires when an event enters the pipeline, before
	// validation or dedup processing.
	HookEventReceived HookType = iota

	// HookEventValidated fires after successful event validation.
	HookEventValidated

	// HookCacheHit fires when the dedup engine finds a hash in the cache.
	HookCacheHit

	// HookCacheMiss fires when the dedup engine must compute a fresh hash.
	HookCacheMiss

	// HookHashComputed fires after a hash is freshly computed by the HashProvider.
	HookHashComputed

	// HookMerkleLeafAdded fires after a leaf is added to a layer's Merkle tree.
	HookMerkleLeafAdded

	// HookLayerSealed fires when a layer's Merkle tree is sealed.
	HookLayerSealed

	// HookTombstone fires when a delete event stores a tombstone.
	HookTombstone

	// HookError fires when any pipeline stage encounters an error.
	HookError

	// HookPipelineStarted fires once when the pipeline begins processing.
	HookPipelineStarted

	// HookPipelineStopped fires once when the pipeline has drained and stopped.
	HookPipelineStopped
)

// String returns a human-readable label.
func (h HookType) String() string {
	switch h {
	case HookEventReceived:
		return "event_received"
	case HookEventValidated:
		return "event_validated"
	case HookCacheHit:
		return "cache_hit"
	case HookCacheMiss:
		return "cache_miss"
	case HookHashComputed:
		return "hash_computed"
	case HookMerkleLeafAdded:
		return "merkle_leaf_added"
	case HookLayerSealed:
		return "layer_sealed"
	case HookTombstone:
		return "tombstone"
	case HookError:
		return "error"
	case HookPipelineStarted:
		return "pipeline_started"
	case HookPipelineStopped:
		return "pipeline_stopped"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(h))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HookEvent
// ─────────────────────────────────────────────────────────────────────────────

// HookEvent carries the context for a single hook invocation.
// Not all fields are populated for every HookType; see field comments.
type HookEvent struct {
	// Type identifies which lifecycle stage triggered this hook.
	Type HookType

	// Event is the FileAccessEvent being processed. Non-nil for per-event hooks.
	// Nil for HookPipelineStarted / HookPipelineStopped.
	Event *event.FileAccessEvent

	// LayerDigest is the output layer for per-event hooks, or the sealed layer
	// for HookLayerSealed. Empty for pipeline-level hooks.
	LayerDigest layer.Digest

	// Hash is the file's content digest (raw bytes). Non-nil for CacheHit,
	// CacheMiss, HashComputed, and MerkleLeafAdded hooks.
	Hash []byte

	// SourceLayer is where the hash was sourced from (cache promotion or
	// fresh computation). Relevant for CacheHit and HashComputed.
	SourceLayer layer.Digest

	// MerkleRoot is set for HookLayerSealed.
	MerkleRoot []byte

	// LeafCount is set for HookLayerSealed.
	LeafCount int

	// Err is non-nil for HookError. It may also be set on other hook types
	// when lenient mode is active to record hook-internal errors.
	Err error

	// Timestamp is when the hook event was created.
	Timestamp time.Time
}

// String returns a compact display string.
func (e HookEvent) String() string {
	if e.Event != nil {
		return fmt.Sprintf("HookEvent{type=%s path=%q layer=%s}", e.Type, e.Event.FilePath, e.LayerDigest)
	}
	return fmt.Sprintf("HookEvent{type=%s layer=%s}", e.Type, e.LayerDigest)
}

// ─────────────────────────────────────────────────────────────────────────────
// Hook interface
// ─────────────────────────────────────────────────────────────────────────────

// Hook is the extension interface for observing the deduplication pipeline.
//
// Implementations receive every HookEvent and may:
//   - Emit metrics or structured logs
//   - Record events for testing
//   - Modify external state (e.g., update a dashboard)
//   - Return errors to fail or skip event processing (strict mode)
//
// Hooks must be goroutine-safe: the pipeline may invoke a hook from any worker.
type Hook interface {
	// OnHook is called for every lifecycle event in the pipeline.
	// The ctx is derived from the pipeline's Run context; honour its deadline.
	// Returning a non-nil error in strict mode fails the current event;
	// in lenient mode the error is recorded and processing continues.
	OnHook(ctx context.Context, e HookEvent) error
}

// HookFunc adapts a plain function to the Hook interface.
type HookFunc func(ctx context.Context, e HookEvent) error

// OnHook implements Hook.
func (f HookFunc) OnHook(ctx context.Context, e HookEvent) error {
	return f(ctx, e)
}

// TypedHook wraps a Hook to only invoke it for specific HookTypes.
// Other types pass through without calling the inner hook.
type TypedHook struct {
	types map[HookType]struct{}
	inner Hook
}

// NewTypedHook creates a TypedHook that fires only for the given types.
func NewTypedHook(inner Hook, types ...HookType) *TypedHook {
	m := make(map[HookType]struct{}, len(types))
	for _, t := range types {
		m[t] = struct{}{}
	}
	return &TypedHook{types: m, inner: inner}
}

// OnHook implements Hook.
func (h *TypedHook) OnHook(ctx context.Context, e HookEvent) error {
	if _, ok := h.types[e.Type]; !ok {
		return nil
	}
	return h.inner.OnHook(ctx, e)
}

// ─────────────────────────────────────────────────────────────────────────────
// HookChain
// ─────────────────────────────────────────────────────────────────────────────

// HookChain executes an ordered sequence of Hooks. It supports both strict
// mode (stop on first error) and lenient mode (continue on error).
//
// Thread safety: HookChain is safe for concurrent use after construction.
// Do not modify the chain after passing it to the pipeline.
type HookChain struct {
	mu      sync.RWMutex
	hooks   []Hook
	lenient bool
}

// NewHookChain creates an empty HookChain. Use WithHook to add hooks.
func NewHookChain(lenient bool, hooks ...Hook) *HookChain {
	hc := &HookChain{lenient: lenient}
	hc.hooks = append(hc.hooks, hooks...)
	return hc
}

// Add appends a Hook to the chain. Thread-safe.
func (hc *HookChain) Add(h Hook) {
	hc.mu.Lock()
	hc.hooks = append(hc.hooks, h)
	hc.mu.Unlock()
}

// Fire invokes all hooks with the given event. In strict mode, the first
// non-nil error causes Fire to return immediately without invoking remaining
// hooks. In lenient mode, all hooks are invoked and the last error is returned.
func (hc *HookChain) Fire(ctx context.Context, e HookEvent) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}

	hc.mu.RLock()
	hooks := make([]Hook, len(hc.hooks))
	copy(hooks, hc.hooks)
	hc.mu.RUnlock()

	var lastErr error
	for _, h := range hooks {
		if err := h.OnHook(ctx, e); err != nil {
			if !hc.lenient {
				return err
			}
			lastErr = err
		}
	}
	return lastErr
}

// Len returns the number of registered hooks.
func (hc *HookChain) Len() int {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return len(hc.hooks)
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in hooks
// ─────────────────────────────────────────────────────────────────────────────

// NoopHook silently discards all events. Useful as a default or in benchmarks
// where hook overhead should be zero.
type NoopHook struct{}

// OnHook implements Hook.
func (NoopHook) OnHook(_ context.Context, _ HookEvent) error { return nil }

// RecordingHook records all received HookEvents for later inspection. Primarily
// intended for integration tests and examples.
type RecordingHook struct {
	mu     sync.RWMutex
	events []HookEvent
}

// NewRecordingHook creates an empty RecordingHook.
func NewRecordingHook() *RecordingHook { return &RecordingHook{} }

// OnHook records the event.
func (h *RecordingHook) OnHook(_ context.Context, e HookEvent) error {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
	return nil
}

// Events returns a snapshot of all recorded events.
func (h *RecordingHook) Events() []HookEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cp := make([]HookEvent, len(h.events))
	copy(cp, h.events)
	return cp
}

// CountByType returns the number of recorded events for the given type.
func (h *RecordingHook) CountByType(t HookType) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var n int
	for _, e := range h.events {
		if e.Type == t {
			n++
		}
	}
	return n
}

// Reset clears all recorded events.
func (h *RecordingHook) Reset() {
	h.mu.Lock()
	h.events = h.events[:0]
	h.mu.Unlock()
}
