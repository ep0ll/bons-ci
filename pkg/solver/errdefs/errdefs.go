// Package errdefs provides structured error types for the solver.
// Errors carry context about which vertex, operation, or solve step
// failed, enabling precise error attribution in complex DAG solves.
package errdefs

import (
	"errors"
	"fmt"

	digest "github.com/opencontainers/go-digest"
)

// VertexError wraps an error with the digest of the vertex that failed.
// This enables error consumers to attribute failures to specific DAG nodes.
type VertexError struct {
	Digest digest.Digest
	Err    error
}

func (e *VertexError) Error() string {
	return fmt.Sprintf("vertex %s: %s", e.Digest, e.Err)
}

func (e *VertexError) Unwrap() error {
	return e.Err
}

// WrapVertex wraps an error with vertex context. Returns nil if err is nil.
func WrapVertex(err error, dgst digest.Digest) error {
	if err == nil {
		return nil
	}
	return &VertexError{Digest: dgst, Err: err}
}

// OpError wraps an error with the operation descriptor that caused it.
type OpError struct {
	Err         error
	Op          any
	Description map[string]string
}

func (e *OpError) Error() string {
	if desc, ok := e.Description["name"]; ok {
		return fmt.Sprintf("op %s: %s", desc, e.Err)
	}
	return fmt.Sprintf("op: %s", e.Err)
}

func (e *OpError) Unwrap() error {
	return e.Err
}

// WrapOp wraps an error with operation context. Returns nil if err is nil.
func WrapOp(err error, op any, description map[string]string) error {
	if err == nil {
		return nil
	}
	return &OpError{Err: err, Op: op, Description: description}
}

// SolveError is returned when a solve operation fails. It carries
// the full solve context including inputs, mount IDs, and the failing op.
type SolveError struct {
	Err         error
	InputIDs    []string
	MountIDs    []string
	Op          any
	Description map[string]string
}

func (e *SolveError) Error() string {
	return e.Err.Error()
}

func (e *SolveError) Unwrap() error {
	return e.Err
}

// WithSolveError wraps an error with solve context retrieved from any
// nested OpError. Returns nil if err is nil.
func WithSolveError(err error, inputIDs, mountIDs []string) error {
	if err == nil {
		return nil
	}
	se := &SolveError{
		Err:      err,
		InputIDs: inputIDs,
		MountIDs: mountIDs,
	}
	// Extract op info from nested OpError if present.
	var oe *OpError
	if AsOpError(err, &oe) {
		se.Op = oe.Op
		se.Description = oe.Description
	}
	return se
}

// AsVertexError extracts a VertexError from an error chain.
func AsVertexError(err error, target **VertexError) bool {
	return errors.As(err, target)
}

// AsOpError extracts an OpError from an error chain.
func AsOpError(err error, target **OpError) bool {
	return errors.As(err, target)
}

// AsSolveError extracts a SolveError from an error chain.
func AsSolveError(err error, target **SolveError) bool {
	return errors.As(err, target)
}
