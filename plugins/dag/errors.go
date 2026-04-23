package reactdag

import (
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// BuildError — top-level build failure envelope
// ---------------------------------------------------------------------------

// BuildError wraps the terminal error of a failed build with structured context.
// Callers can use errors.As to inspect the underlying cause.
type BuildError struct {
	TargetID string
	Cause    error
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("build %q failed: %v", e.TargetID, e.Cause)
}

func (e *BuildError) Unwrap() error { return e.Cause }

// NewBuildError wraps cause in a BuildError for the given target.
func NewBuildError(targetID string, cause error) *BuildError {
	return &BuildError{TargetID: targetID, Cause: cause}
}

// ---------------------------------------------------------------------------
// VertexError — execution failure for a specific vertex
// ---------------------------------------------------------------------------

// VertexError records the failure of a single vertex operation.
type VertexError struct {
	VertexID string
	OpID     string
	Attempt  int // 1-based attempt number (>1 if retried)
	Cause    error
}

func (e *VertexError) Error() string {
	if e.Attempt > 1 {
		return fmt.Sprintf("vertex %q (op=%s attempt=%d): %v",
			e.VertexID, e.OpID, e.Attempt, e.Cause)
	}
	return fmt.Sprintf("vertex %q (op=%s): %v", e.VertexID, e.OpID, e.Cause)
}

func (e *VertexError) Unwrap() error { return e.Cause }

// NewVertexError constructs a VertexError.
func NewVertexError(v *Vertex, attempt int, cause error) *VertexError {
	return &VertexError{
		VertexID: v.ID(),
		OpID:     v.OpID(),
		Attempt:  attempt,
		Cause:    cause,
	}
}

// ---------------------------------------------------------------------------
// CacheError — cache tier failure
// ---------------------------------------------------------------------------

// CacheError records a failure in a specific cache tier operation.
type CacheError struct {
	Tier      string // "fast" or "slow"
	Operation string // "get", "set", "delete"
	VertexID  string
	Cause     error
}

func (e *CacheError) Error() string {
	return fmt.Sprintf("cache[%s].%s for vertex %q: %v",
		e.Tier, e.Operation, e.VertexID, e.Cause)
}

func (e *CacheError) Unwrap() error { return e.Cause }

// ---------------------------------------------------------------------------
// InvalidationError — invalidation phase failure
// ---------------------------------------------------------------------------

// InvalidationError records a failure during the file-change invalidation pass.
type InvalidationError struct {
	ChangedFile string
	Cause       error
}

func (e *InvalidationError) Error() string {
	return fmt.Sprintf("invalidation of %q failed: %v", e.ChangedFile, e.Cause)
}

func (e *InvalidationError) Unwrap() error { return e.Cause }

// ---------------------------------------------------------------------------
// CycleError — detected during DAG construction
// ---------------------------------------------------------------------------

// CycleError is returned by DAG.Seal when a cycle is detected.
type CycleError struct {
	InvolvedVertex string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("cycle detected involving vertex %q", e.InvolvedVertex)
}

// ---------------------------------------------------------------------------
// StateTransitionError — illegal state-machine edge
// ---------------------------------------------------------------------------

// StateTransitionError is returned when a state transition is not allowed.
type StateTransitionError struct {
	VertexID string
	From     State
	To       State
	Cause    string
}

func (e *StateTransitionError) Error() string {
	return fmt.Sprintf("vertex %q: illegal transition %s → %s (%s)",
		e.VertexID, e.From, e.To, e.Cause)
}

// ---------------------------------------------------------------------------
// Error inspection helpers
// ---------------------------------------------------------------------------

// IsVertexError reports whether err (or any of its chain) is a *VertexError.
func IsVertexError(err error) bool {
	var ve *VertexError
	return errors.As(err, &ve)
}

// IsBuildError reports whether err is a *BuildError.
func IsBuildError(err error) bool {
	var be *BuildError
	return errors.As(err, &be)
}

// IsCacheError reports whether err is a *CacheError.
func IsCacheError(err error) bool {
	var ce *CacheError
	return errors.As(err, &ce)
}

// VertexIDFromError extracts the VertexID from a *VertexError, returning ""
// if the error chain contains no VertexError.
func VertexIDFromError(err error) string {
	var ve *VertexError
	if errors.As(err, &ve) {
		return ve.VertexID
	}
	return ""
}

// CollectVertexErrors walks an error tree (using errors.Join semantics) and
// returns all *VertexError values found.
func CollectVertexErrors(err error) []*VertexError {
	if err == nil {
		return nil
	}
	var out []*VertexError
	var ve *VertexError
	if errors.As(err, &ve) {
		out = append(out, ve)
	}
	// Walk Unwrap chain.
	var uw interface{ Unwrap() []error }
	if errors.As(err, &uw) {
		for _, child := range uw.Unwrap() {
			out = append(out, CollectVertexErrors(child)...)
		}
	}
	return out
}
