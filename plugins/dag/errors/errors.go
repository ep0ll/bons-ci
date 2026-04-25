// Package errors provides structured error types for the DAG library.
//
// All errors produced by this library wrap one of these typed errors,
// allowing external systems (solvers, caches, UI renderers) to inspect
// failure modes with errors.As and react specifically rather than parsing
// error strings.
//
// Example:
//
//	dag, err := graph.BuildFromRoot(ctx, root)
//	if err != nil {
//	    var cycleErr *errors.CycleError
//	    if errors.As(err, &cycleErr) {
//	        fmt.Printf("cycle detected between %v\n", cycleErr.Vertices)
//	    }
//	}
package errors

import (
	"fmt"
	"strings"
)

// ─── CycleError ───────────────────────────────────────────────────────────────

// CycleError is returned when cycle detection finds a back-edge in the graph.
// Vertices contains the IDs forming the cycle (first == last to show closure).
type CycleError struct {
	// Vertices is the cycle path: V[0] → V[1] → … → V[n] where V[0] == V[n].
	Vertices []string
	// Message is a human-readable description.
	Message string
}

func (e *CycleError) Error() string {
	if e.Message != "" {
		return "graph: cycle detected: " + e.Message
	}
	return "graph: cycle detected: " + strings.Join(e.Vertices, " → ")
}

// NewCycleError constructs a CycleError from a path of vertex IDs.
func NewCycleError(path []string) *CycleError {
	if len(path) > 0 {
		closed := make([]string, len(path)+1)
		copy(closed, path)
		closed[len(path)] = path[0]
		return &CycleError{Vertices: closed, Message: strings.Join(closed, " → ")}
	}
	return &CycleError{Message: "unknown cycle"}
}

// ─── ValidationError ──────────────────────────────────────────────────────────

// ValidationError is returned when a vertex fails its Validate check.
type ValidationError struct {
	// VertexID is the ID of the failing vertex.
	VertexID string
	// VertexKind is the Kind of the failing vertex.
	VertexKind string
	// Cause is the underlying validation failure.
	Cause error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("graph: validation failed for vertex %q (%s): %v",
		e.VertexID, e.VertexKind, e.Cause)
}

func (e *ValidationError) Unwrap() error { return e.Cause }

// NewValidationError wraps a validation failure with vertex context.
func NewValidationError(id, kind string, cause error) *ValidationError {
	return &ValidationError{VertexID: id, VertexKind: kind, Cause: cause}
}

// ─── IDCollisionError ─────────────────────────────────────────────────────────

// IDCollisionError is returned when two distinct vertex objects share the same ID.
// This indicates a bug in the vertex's ID computation.
type IDCollisionError struct {
	ID         string
	KindA      string
	KindB      string
}

func (e *IDCollisionError) Error() string {
	return fmt.Sprintf("graph: ID collision — two distinct vertices share ID %q (kinds: %q vs %q)",
		e.ID, e.KindA, e.KindB)
}

// ─── SerializationError ───────────────────────────────────────────────────────

// SerializationError is returned when a vertex cannot be serialized.
type SerializationError struct {
	VertexID   string
	VertexKind string
	Cause      error
}

func (e *SerializationError) Error() string {
	return fmt.Sprintf("marshal: vertex %q (%s): %v", e.VertexID, e.VertexKind, e.Cause)
}

func (e *SerializationError) Unwrap() error { return e.Cause }

// ─── TraversalError ───────────────────────────────────────────────────────────

// TraversalError is returned when a hook function returns an error during traversal.
type TraversalError struct {
	// Hook identifies which hook type triggered the error: "pre", "post", "edge".
	Hook string
	// VertexID is the vertex being visited when the error occurred.
	VertexID string
	// Depth is the traversal depth at the point of failure.
	Depth int
	// Cause is the underlying error from the hook.
	Cause error
}

func (e *TraversalError) Error() string {
	return fmt.Sprintf("traverse: %s hook error at depth %d (vertex %q): %v",
		e.Hook, e.Depth, e.VertexID, e.Cause)
}

func (e *TraversalError) Unwrap() error { return e.Cause }

// ─── UnknownVertexKindError ───────────────────────────────────────────────────

// UnknownVertexKindError is returned when the marshaler encounters a vertex
// Kind for which no serializer is registered.
type UnknownVertexKindError struct {
	Kind string
}

func (e *UnknownVertexKindError) Error() string {
	return fmt.Sprintf("marshal: no serializer registered for vertex kind %q", e.Kind)
}
