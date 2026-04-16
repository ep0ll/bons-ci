// Package core defines the foundational abstractions for the pluggable
// exporter framework. All exporters, transformers, and pipeline components
// depend only on this package — never on each other — enforcing clean
// separation of concerns and enabling independent extension.
package core

import (
	"context"
	"io"
)

// ─── Exporter hierarchy ────────────────────────────────────────────────────

// Exporter is a stateless, concurrency-safe factory that creates per-operation
// ExporterInstance values. One Exporter is registered per type; one
// ExporterInstance is created per export invocation.
//
// Implementing a new backend (deb, apt, Dallie, Helm, etc.) means satisfying
// this interface and registering via Registry.Register.
type Exporter interface {
	// Type returns the unique, immutable identifier for this exporter.
	Type() ExporterType

	// Resolve validates opts, applies defaults, and returns a configured
	// ExporterInstance ready to Export. Must be safe for concurrent calls.
	Resolve(ctx context.Context, opts Options) (ExporterInstance, error)
}

// ExporterInstance performs a single, fully-configured export operation.
// Implementations are NOT required to be concurrency-safe; callers must not
// share an instance across goroutines.
type ExporterInstance interface {
	// Export executes the primary export work. It may return a FinalizeFunc
	// for deferred, async work (e.g., registry push) that runs after all
	// sibling exports have completed their own Export phases.
	//
	// If FinalizeFunc is nil, all work is complete when Export returns.
	// A non-nil FinalizeFunc MUST eventually be called by the caller;
	// however, not calling it must not leak resources.
	Export(ctx context.Context, req *ExportRequest) (*ExportResult, FinalizeFunc, error)
}

// FinalizeFunc completes deferred work that must occur after all sibling
// exports have finished their Export phase. Typical use: pushing an image
// manifest after all layers have been committed.
//
// Safe to call concurrently with other FinalizeFunc values.
type FinalizeFunc func(ctx context.Context) error

// ─── Transformer hierarchy ─────────────────────────────────────────────────

// Transformer is a single, composable unit of pre/post-processing applied
// to an Artifact within an export Pipeline. Transformers are ordered by
// Priority and composed via TransformerChain.
//
// Examples: epoch normalization, SBOM supplementation, annotation injection,
// signature attachment, vulnerability scanning, etc.
type Transformer interface {
	// Name is a unique, human-readable identifier used in logs and traces.
	Name() string

	// Transform receives an Artifact and returns a (possibly new) Artifact.
	// Returning the same pointer is valid; returning a new one is preferred
	// for immutability.
	Transform(ctx context.Context, artifact *Artifact) (*Artifact, error)

	// Priority controls ordering: lower values run first.
	// Suggested ranges: 0–99 pre-processing, 100–199 core, 200+ post-processing.
	Priority() int
}

// ─── Progress reporting ────────────────────────────────────────────────────

// ProgressReporter is the observer for real-time export progress. Callers
// inject a concrete implementation (console, gRPC stream, no-op, etc.) so
// that exporters have zero knowledge of the delivery mechanism.
type ProgressReporter interface {
	// Start announces the beginning of a named operation.
	Start(ctx context.Context, id string, label string)

	// Update emits a mid-operation progress value (0–100).
	Update(ctx context.Context, id string, pct int)

	// Complete marks the operation as finished. err may be nil on success.
	Complete(ctx context.Context, id string, err error)

	// io.Closer allows the reporter to flush and free resources.
	io.Closer
}

// ─── Content store ─────────────────────────────────────────────────────────

// ContentWriter is the write side of an addressable content store.
// The container-image exporter uses it to persist blobs; other exporters
// may ignore it entirely.
type ContentWriter interface {
	// WriteBlob persists data and returns the content address.
	WriteBlob(ctx context.Context, data []byte, mediaType string) (Digest, error)

	// Has reports whether the given digest is already stored, enabling
	// deduplication before expensive write operations.
	Has(ctx context.Context, d Digest) (bool, error)
}

// ContentReader is the read side of the content store.
type ContentReader interface {
	// ReadBlob retrieves blob bytes by digest.
	ReadBlob(ctx context.Context, d Digest) ([]byte, error)
}

// ContentStore combines read and write access.
type ContentStore interface {
	ContentWriter
	ContentReader
}
