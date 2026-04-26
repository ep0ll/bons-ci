package core

import (
	"errors"
	"fmt"

	digest "github.com/opencontainers/go-digest"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	ErrVertexNotFound     = errors.New("vertex not found in graph")
	ErrCyclicGraph        = errors.New("mutation would create a cycle in the graph")
	ErrIncompatibleInputs = errors.New("inputs are incompatible with this vertex type")
	ErrInvalidOutputIndex = errors.New("output index out of range")
	ErrEmptyGraph         = errors.New("graph has no reachable vertices")
	ErrMarshalFailed      = errors.New("vertex marshal failed")
	ErrValidationFailed   = errors.New("vertex validation failed")
	ErrPolicyRejected         = errors.New("gate policy rejected the vertex")
	ErrNoMatch                = errors.New("no candidate matched the selector criteria")
	ErrSolveFailed            = errors.New("solve operation failed")
	ErrDynEvalFailed          = errors.New("dynamic policy evaluation failed")
	ErrExportFailed           = errors.New("export operation failed")
	ErrUnsupportedExportFormat = errors.New("unsupported export format")
)

// ─── Typed errors ─────────────────────────────────────────────────────────────

type VertexNotFoundError struct{ ID VertexID }

func (e *VertexNotFoundError) Error() string   { return fmt.Sprintf("vertex %q not found", e.ID) }
func (e *VertexNotFoundError) Is(t error) bool { return t == ErrVertexNotFound }

type CyclicGraphError struct{ Cycle []VertexID }

func (e *CyclicGraphError) Error() string   { return fmt.Sprintf("cycle detected: %v", e.Cycle) }
func (e *CyclicGraphError) Is(t error) bool { return t == ErrCyclicGraph }

type IncompatibleInputsError struct {
	VertexType VertexType
	Got        int
	Want       string
	Detail     string
}

func (e *IncompatibleInputsError) Error() string {
	msg := fmt.Sprintf("vertex %q expects %s inputs, got %d", e.VertexType, e.Want, e.Got)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}
func (e *IncompatibleInputsError) Is(t error) bool { return t == ErrIncompatibleInputs }

type ValidationError struct {
	VertexID VertexID
	Field    string
	Cause    error
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("validation of vertex %q field %q: %v", e.VertexID, e.Field, e.Cause)
	}
	return fmt.Sprintf("validation of vertex %q: %v", e.VertexID, e.Cause)
}
func (e *ValidationError) Unwrap() error   { return e.Cause }
func (e *ValidationError) Is(t error) bool { return t == ErrValidationFailed }

type MarshalError struct {
	VertexID VertexID
	Cause    error
}

func (e *MarshalError) Error() string {
	return fmt.Sprintf("marshal vertex %q: %v", e.VertexID, e.Cause)
}
func (e *MarshalError) Unwrap() error   { return e.Cause }
func (e *MarshalError) Is(t error) bool { return t == ErrMarshalFailed }

type PolicyRejectedError struct {
	VertexID VertexID
	Policy   string
	Reason   string
}

func (e *PolicyRejectedError) Error() string {
	return fmt.Sprintf("gate policy %q rejected vertex %q: %s", e.Policy, e.VertexID, e.Reason)
}
func (e *PolicyRejectedError) Is(t error) bool { return t == ErrPolicyRejected }

type NoMatchError struct {
	SelectorID VertexID
	Criteria   string
}

func (e *NoMatchError) Error() string {
	return fmt.Sprintf("selector %q: no candidate matched %q", e.SelectorID, e.Criteria)
}
func (e *NoMatchError) Is(t error) bool { return t == ErrNoMatch }

type SolveError struct {
	Cause error
}

func (e *SolveError) Error() string   { return fmt.Sprintf("solve: %v", e.Cause) }
func (e *SolveError) Unwrap() error   { return e.Cause }
func (e *SolveError) Is(t error) bool { return t == ErrSolveFailed }

type DynEvalError struct {
	VertexID VertexID
	Cause    error
}

func (e *DynEvalError) Error() string   { return fmt.Sprintf("dyn eval vertex %q: %v", e.VertexID, e.Cause) }
func (e *DynEvalError) Unwrap() error   { return e.Cause }
func (e *DynEvalError) Is(t error) bool { return t == ErrDynEvalFailed }

type ExportError struct {
	Format string
	Cause  error
}

func (e *ExportError) Error() string   { return fmt.Sprintf("export %q: %v", e.Format, e.Cause) }
func (e *ExportError) Unwrap() error   { return e.Cause }
func (e *ExportError) Is(t error) bool { return t == ErrExportFailed }

// ─── Helpers ──────────────────────────────────────────────────────────────────

func WrapMarshal(id digest.Digest, err error) error {
	if err == nil {
		return nil
	}
	return &MarshalError{VertexID: id, Cause: err}
}

func WrapValidation(id digest.Digest, field string, err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{VertexID: id, Field: field, Cause: err}
}
