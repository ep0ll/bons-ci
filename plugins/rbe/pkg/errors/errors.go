package errors

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrAlreadyExists       = errors.New("already exists")
	ErrInvalidDigest       = errors.New("invalid digest")
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrUploadNotFound      = errors.New("upload session not found")
	ErrUploadExpired       = errors.New("upload session expired")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrRangeNotSatisfiable = errors.New("range not satisfiable")
	ErrCacheMiss           = errors.New("cache miss")
	ErrLockConflict        = errors.New("lock conflict")
	ErrStreamClosed        = errors.New("stream closed")
	ErrBackendFailure      = errors.New("storage backend failure")
	ErrDAGNotFound         = errors.New("DAG not found")
	ErrVertexNotFound      = errors.New("vertex not found")
)

// RBEError carries an OCI-compatible error code, HTTP status, and optional cause.
type RBEError struct {
	Code       string
	Message    string
	Detail     interface{}
	HTTPStatus int
	Cause      error
}

func (e *RBEError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *RBEError) Unwrap() error { return e.Cause }

func (e *RBEError) Is(target error) bool {
	t, ok := target.(*RBEError)
	if !ok {
		return errors.Is(e.Cause, target)
	}
	return e.Code == t.Code
}

func NewBlobUnknown(digest string) *RBEError {
	return &RBEError{Code: "BLOB_UNKNOWN", Message: "blob unknown: " + digest, HTTPStatus: http.StatusNotFound, Cause: ErrNotFound}
}
func NewManifestUnknown(ref string) *RBEError {
	return &RBEError{Code: "MANIFEST_UNKNOWN", Message: "manifest unknown: " + ref, HTTPStatus: http.StatusNotFound, Cause: ErrNotFound}
}
func NewNameUnknown(name string) *RBEError {
	return &RBEError{Code: "NAME_UNKNOWN", Message: "repository name not known: " + name, HTTPStatus: http.StatusNotFound, Cause: ErrNotFound}
}
func NewDigestInvalid(d string) *RBEError {
	return &RBEError{Code: "DIGEST_INVALID", Message: "digest mismatch: " + d, HTTPStatus: http.StatusBadRequest, Cause: ErrInvalidDigest}
}
func NewSizeInvalid() *RBEError {
	return &RBEError{Code: "SIZE_INVALID", Message: "content length mismatch", HTTPStatus: http.StatusBadRequest}
}
func NewUploadUnknown(uuid string) *RBEError {
	return &RBEError{Code: "BLOB_UPLOAD_UNKNOWN", Message: "upload unknown: " + uuid, HTTPStatus: http.StatusNotFound, Cause: ErrUploadNotFound}
}
func NewUnauthorized() *RBEError {
	return &RBEError{Code: "UNAUTHORIZED", Message: "authentication required", HTTPStatus: http.StatusUnauthorized, Cause: ErrUnauthorized}
}
func NewDenied() *RBEError {
	return &RBEError{Code: "DENIED", Message: "access denied", HTTPStatus: http.StatusForbidden, Cause: ErrForbidden}
}

func New(msg string) error              { return errors.New(msg) }
func Is(err, target error) bool         { return errors.Is(err, target) }
func As(err error, t interface{}) bool  { return errors.As(err, t) }
func Wrap(err error, msg string) error  { return fmt.Errorf("%s: %w", msg, err) }
func Wrapf(err error, f string, a ...interface{}) error {
	return fmt.Errorf(f+": %w", append(a, err)...)
}
