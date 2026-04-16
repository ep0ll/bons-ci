package core

import (
	"errors"
	"fmt"

	digest "github.com/opencontainers/go-digest"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrVertexNotFound is returned when a lookup by VertexID yields nothing.
	ErrVertexNotFound = errors.New("vertex not found in graph")

	// ErrCyclicGraph is returned when a mutation would introduce a cycle.
	ErrCyclicGraph = errors.New("mutation would create a cycle in the graph")

	// ErrIncompatibleInputs is returned when a vertex cannot accept the
	// provided edges (wrong count, wrong types, etc.).
	ErrIncompatibleInputs = errors.New("inputs are incompatible with this vertex type")

	// ErrInvalidOutputIndex is returned when an edge references an output slot
	// that does not exist on the producer vertex.
	ErrInvalidOutputIndex = errors.New("output index out of range")

	// ErrEmptyGraph is returned when serialisation is attempted on an empty
	// (scratch-only) graph.
	ErrEmptyGraph = errors.New("graph has no vertices")

	// ErrMarshalFailed wraps any serialisation failure.
	ErrMarshalFailed = errors.New("vertex marshal failed")

	// ErrValidationFailed wraps any validation failure.
	ErrValidationFailed = errors.New("vertex validation failed")
)

// ─── Typed error types ────────────────────────────────────────────────────────

// VertexNotFoundError carries the ID that could not be located.
type VertexNotFoundError struct {
	ID VertexID
}

func (e *VertexNotFoundError) Error() string {
	return fmt.Sprintf("vertex %q not found in graph", e.ID)
}
func (e *VertexNotFoundError) Is(target error) bool {
	return target == ErrVertexNotFound
}

// IncompatibleInputsError describes a bad rewiring attempt.
type IncompatibleInputsError struct {
	VertexType VertexType
	Got        int
	Want       string // human description e.g. "exactly 2" or "at least 1"
	Detail     string
}

func (e *IncompatibleInputsError) Error() string {
	msg := fmt.Sprintf("vertex type %q expects %s inputs, got %d", e.VertexType, e.Want, e.Got)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}
func (e *IncompatibleInputsError) Is(target error) bool {
	return target == ErrIncompatibleInputs
}

// InvalidOutputIndexError carries the offending index and the vertex's capacity.
type InvalidOutputIndexError struct {
	VertexID VertexID
	Index    int
	Max      int
}

func (e *InvalidOutputIndexError) Error() string {
	return fmt.Sprintf("output index %d out of range for vertex %q (max %d)", e.Index, e.VertexID, e.Max)
}
func (e *InvalidOutputIndexError) Is(target error) bool {
	return target == ErrInvalidOutputIndex
}

// CyclicGraphError names the vertices that form the cycle.
type CyclicGraphError struct {
	Cycle []VertexID
}

func (e *CyclicGraphError) Error() string {
	return fmt.Sprintf("cycle detected: %v", e.Cycle)
}
func (e *CyclicGraphError) Is(target error) bool {
	return target == ErrCyclicGraph
}

// MarshalError wraps a low-level serialisation failure with vertex context.
type MarshalError struct {
	VertexID VertexID
	Cause    error
}

func (e *MarshalError) Error() string {
	return fmt.Sprintf("marshal vertex %q: %v", e.VertexID, e.Cause)
}
func (e *MarshalError) Unwrap() error { return e.Cause }
func (e *MarshalError) Is(target error) bool {
	return target == ErrMarshalFailed
}

// ValidationError wraps a validation failure with vertex context.
type ValidationError struct {
	VertexID VertexID
	Field    string
	Cause    error
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("validation failed for vertex %q field %q: %v", e.VertexID, e.Field, e.Cause)
	}
	return fmt.Sprintf("validation failed for vertex %q: %v", e.VertexID, e.Cause)
}
func (e *ValidationError) Unwrap() error { return e.Cause }
func (e *ValidationError) Is(target error) bool {
	return target == ErrValidationFailed
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// WrapMarshal wraps err as a MarshalError for the given digest. Nil-safe.
func WrapMarshal(id digest.Digest, err error) error {
	if err == nil {
		return nil
	}
	return &MarshalError{VertexID: id, Cause: err}
}

// WrapValidation wraps err as a ValidationError. Nil-safe.
func WrapValidation(id digest.Digest, field string, err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{VertexID: id, Field: field, Cause: err}
}
