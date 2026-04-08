package domain

import (
	"errors"
	"fmt"
)

// ErrorKind classifies a domain error for programmatic handling.
type ErrorKind string

const (
	ErrKindValidation ErrorKind = "validation"
	ErrKindNotFound   ErrorKind = "not_found"
	ErrKindScanning   ErrorKind = "scanning"
	ErrKindExporting  ErrorKind = "exporting"
	ErrKindResolving  ErrorKind = "resolving"
	ErrKindAuth       ErrorKind = "auth"
	ErrKindTimeout    ErrorKind = "timeout"
	ErrKindInternal   ErrorKind = "internal"
)

// Error is the canonical domain error.
// It implements error and supports errors.Is / errors.As via Unwrap.
type Error struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Kind, e.Message)
}

// Unwrap enables errors.Is and errors.As to traverse the causal chain.
func (e *Error) Unwrap() error { return e.Cause }

// Is returns true if target is an *Error with the same Kind.
// This allows callers to write errors.Is(err, &domain.Error{Kind: domain.ErrKindAuth}).
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return e.Kind == t.Kind
	}
	return false
}

// New creates a domain Error.
func New(kind ErrorKind, msg string, cause error) *Error {
	return &Error{Kind: kind, Message: msg, Cause: cause}
}

// Newf creates a domain Error with a formatted message.
func Newf(kind ErrorKind, cause error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Cause: cause}
}

// Sentinel errors for use with errors.Is.
var (
	ErrValidation = &Error{Kind: ErrKindValidation}
	ErrNotFound   = &Error{Kind: ErrKindNotFound}
	ErrScanning   = &Error{Kind: ErrKindScanning}
	ErrExporting  = &Error{Kind: ErrKindExporting}
	ErrResolving  = &Error{Kind: ErrKindResolving}
	ErrAuth       = &Error{Kind: ErrKindAuth}
	ErrTimeout    = &Error{Kind: ErrKindTimeout}
	ErrInternal   = &Error{Kind: ErrKindInternal}
)

// IsKind returns true if err (or any error in its chain) is a *domain.Error
// with the given kind.
func IsKind(err error, kind ErrorKind) bool {
	var de *Error
	if errors.As(err, &de) {
		return de.Kind == kind
	}
	return false
}
