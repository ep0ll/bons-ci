package registry

import (
	"fmt"

	"github.com/containerd/errdefs"
)

// ErrKind classifies registry store errors so callers can branch without
// inspecting error strings. Use [errors.As] to extract from an error chain.
type ErrKind int

const (
	ErrKindUnknown         ErrKind = iota
	ErrKindNotFound                // content absent locally and remotely
	ErrKindAlreadyExists           // duplicate ingestion or content
	ErrKindInvalidArgument         // bad input from caller
	ErrKindUnavailable             // transient registry / network failure
	ErrKindPrecondition            // size or digest mismatch (e.g. double-commit)
)

// StoreError is a classified, chain-able error from a registry store operation.
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

// Unwrap enables errors.Is / errors.As traversal.
func (e *StoreError) Unwrap() error { return e.Cause }

// storeErr constructs a classified StoreError.
func storeErr(kind ErrKind, op, msg string, cause error) *StoreError {
	return &StoreError{Kind: kind, Op: op, Message: msg, Cause: cause}
}

// notFoundErr wraps cause with errdefs.ErrNotFound so containerd callers can
// use errdefs.IsNotFound.
func notFoundErr(op string, cause error) error {
	return fmt.Errorf("registry.%s: %v: %w", op, cause, errdefs.ErrNotFound)
}
