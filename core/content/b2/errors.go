package b2

import (
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/minio/minio-go/v7"
)

// ---------------------------------------------------------------------------
// Error Classification
// ---------------------------------------------------------------------------

// ErrKind classifies store errors for callers to branch on.
type ErrKind int

const (
	ErrKindUnknown         ErrKind = iota
	ErrKindNotFound                // object or bucket does not exist
	ErrKindAlreadyExists           // object already exists (conflict)
	ErrKindInvalidArgument         // bad input from caller
	ErrKindUnavailable             // transient backend failure
	ErrKindPrecondition            // precondition (size, digest) not met
)

// StoreError is a classified error from a B2 store operation.
type StoreError struct {
	Kind    ErrKind
	Op      string // operation name, e.g. "Info", "Delete"
	Message string
	Cause   error
}

func (e *StoreError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("b2.%s: %s: %v", e.Op, e.Message, e.Cause)
	}
	return fmt.Sprintf("b2.%s: %s", e.Op, e.Message)
}

func (e *StoreError) Unwrap() error { return e.Cause }

// storeErr creates a classified StoreError.
func storeErr(kind ErrKind, op, msg string, cause error) *StoreError {
	return &StoreError{Kind: kind, Op: op, Message: msg, Cause: cause}
}

// wrapNotFound wraps a cause as a not-found StoreError that also unwraps
// to errdefs.ErrNotFound for containerd compatibility.
func wrapNotFound(op string, cause error) error {
	return storeErr(ErrKindNotFound, op,
		fmt.Sprintf("%v: %v", cause, errdefs.ErrNotFound), cause)
}

// classifyMinioErr maps a minio error response to an ErrKind.
func classifyMinioErr(err error) ErrKind {
	resp := minio.ToErrorResponse(err)
	switch {
	case resp.StatusCode == 404 || resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket":
		return ErrKindNotFound
	case resp.StatusCode == 409:
		return ErrKindAlreadyExists
	case resp.StatusCode == 400:
		return ErrKindInvalidArgument
	case resp.StatusCode >= 500:
		return ErrKindUnavailable
	default:
		return ErrKindUnknown
	}
}

// isNotFound checks whether an error indicates a missing object.
func isNotFound(err error) bool {
	return classifyMinioErr(err) == ErrKindNotFound
}
