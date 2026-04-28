// Package fshash provides a Merkle tree deduplication engine for layer-scoped
// file access tracking. It eliminates redundant hash computations across stacked
// container filesystem layers by maintaining a content-addressed Merkle tree of
// accessed files with copy-on-write deduplication semantics.
//
// The package is event-driven: it receives [AccessEvent] values from a fanotify
// channel and produces per-layer Merkle roots, deduplicating hash computations
// across the entire layer stack.
package fshash

import (
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// Re-export all domain types from internal/core so consumers use
// "fshash.LayerID" without knowing about the internal package.

// LayerID uniquely identifies a filesystem layer by its content digest.
type LayerID = core.LayerID

// VertexID uniquely identifies an execution vertex (ExecOp) by its digest.
type VertexID = core.VertexID

// AccessOp classifies the type of filesystem operation observed.
type AccessOp = core.AccessOp

// AccessEvent represents a single filesystem access event from fanotify.
type AccessEvent = core.AccessEvent

// FileHash represents the computed content hash of a file.
type FileHash = core.FileHash

// Action describes the deduplication decision for an access event.
type Action = core.Action

// DeduplicationResult captures the outcome of processing an AccessEvent.
type DeduplicationResult = core.DeduplicationResult

// ProcessorStats aggregates operational statistics.
type ProcessorStats = core.ProcessorStats

// Re-export constructors.
var (
	NewLayerID  = core.NewLayerID
	NewVertexID = core.NewVertexID
)

// Re-export operation constants.
const (
	OpRead  = core.OpRead
	OpWrite = core.OpWrite
	OpClose = core.OpClose
)

// Re-export action constants.
const (
	ActionCompute = core.ActionCompute
	ActionReuse   = core.ActionReuse
	ActionSkip    = core.ActionSkip
)

// Re-export sentinel errors.
var (
	ErrLayerNotFound = core.ErrLayerNotFound
	ErrLayerExists   = core.ErrLayerExists
	ErrCacheMiss     = core.ErrCacheMiss
	ErrTreeEmpty     = core.ErrTreeEmpty
	ErrInvalidEvent  = core.ErrInvalidEvent
	ErrClosed        = core.ErrClosed
	ErrInvalidChain  = core.ErrInvalidChain
)
