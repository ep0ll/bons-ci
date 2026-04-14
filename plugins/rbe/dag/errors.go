package dagstore

import (
	"errors"
	"fmt"
)

// Sentinel errors — use errors.Is to test.
var (
	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists is returned when a resource already exists and cannot be overwritten.
	ErrAlreadyExists = errors.New("already exists")
	// ErrInvalidArgument is returned when a caller supplies invalid input.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrInternal is returned for unexpected internal failures.
	ErrInternal = errors.New("internal error")
	// ErrClosed is returned when an operation is attempted on a closed store.
	ErrClosed = errors.New("store is closed")
	// ErrIntegrityViolation is returned when a hash check fails.
	ErrIntegrityViolation = errors.New("integrity violation")
)

// ——— typed errors ————————————————————————————————————————————————————————————

// NotFoundError carries the kind and identity of the missing resource.
type NotFoundError struct {
	Kind string // "dag", "vertex", "stream", "id"
	ID   string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q: %v", e.Kind, e.ID, ErrNotFound)
}

func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// AlreadyExistsError carries the kind and identity of the duplicate resource.
type AlreadyExistsError struct {
	Kind string
	ID   string
}

func (e *AlreadyExistsError) Error() string {
	return fmt.Sprintf("%s %q: %v", e.Kind, e.ID, ErrAlreadyExists)
}

func (e *AlreadyExistsError) Is(target error) bool { return target == ErrAlreadyExists }

// InvalidArgumentError carries a human-readable reason.
type InvalidArgumentError struct {
	Field  string
	Reason string
}

func (e *InvalidArgumentError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("invalid argument %q: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("invalid argument: %s", e.Reason)
}

func (e *InvalidArgumentError) Is(target error) bool { return target == ErrInvalidArgument }

// IntegrityError is returned when the stored hash does not match the recomputed hash.
type IntegrityError struct {
	Kind     string // "dag", "vertex", "file"
	ID       string
	Expected string
	Got      string
}

func (e *IntegrityError) Error() string {
	return fmt.Sprintf("integrity check failed for %s %q: expected %q, got %q",
		e.Kind, e.ID, e.Expected, e.Got)
}

func (e *IntegrityError) Is(target error) bool { return target == ErrIntegrityViolation }

// InternalError wraps an unexpected underlying error.
type InternalError struct {
	Op  string
	Err error
}

func (e *InternalError) Error() string {
	return fmt.Sprintf("internal error during %q: %v", e.Op, e.Err)
}

func (e *InternalError) Unwrap() error              { return e.Err }
func (e *InternalError) Is(target error) bool       { return target == ErrInternal }
