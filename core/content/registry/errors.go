package registry

import (
	"fmt"

	"github.com/containerd/errdefs"
)

// ---------------------------------------------------------------------------
// Error Classification
// ---------------------------------------------------------------------------

// ErrKind classifies store errors for callers to branch on without inspecting
// error strings.
type ErrKind int

const (
	ErrKindUnknown         ErrKind = iota
	ErrKindNotFound                // content does not exist locally or remotely
	ErrKindAlreadyExists           // duplicate ingestion or content
	ErrKindInvalidArgument         // bad input from caller
	ErrKindUnavailable             // transient registry/network failure
	ErrKindPrecondition            // size or digest mismatch
)

// StoreError is a classified error from a registry store operation.
type StoreError struct {
	Kind    ErrKind
	Op      string // operation name, e.g. "Info", "Writer"
	Message string
	Cause   error
}

func (e *StoreError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("registry.%s: %s: %v", e.Op, e.Message, e.Cause)
	}
	return fmt.Sprintf("registry.%s: %s", e.Op, e.Message)
}

func (e *StoreError) Unwrap() error { return e.Cause }

// storeErr creates a classified StoreError.
func storeErr(kind ErrKind, op, msg string, cause error) *StoreError {
	return &StoreError{Kind: kind, Op: op, Message: msg, Cause: cause}
}

// notFoundErr wraps a cause as a not-found error compatible with errdefs.
func notFoundErr(op string, cause error) error {
	return fmt.Errorf("registry.%s: %v: %w", op, cause, errdefs.ErrNotFound)
}
