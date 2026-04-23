// Package errors defines the typed error hierarchy for AccelRegistry.
// All domain errors satisfy the standard error interface and carry
// an HTTP status code for use by the OCI Distribution Spec handlers.
package errors

import (
	"errors"
	"fmt"
	"net/http"

	digest "github.com/opencontainers/go-digest"
)

// ────────────────────────────────────────────────────────────────────────────
// OCI Distribution Spec error codes
// Ref: https://github.com/opencontainers/distribution-spec/blob/main/spec.md
// ────────────────────────────────────────────────────────────────────────────

// Code is an OCI Distribution Spec error code string.
type Code string

const (
	CodeBlobUnknown         Code = "BLOB_UNKNOWN"
	CodeBlobUploadInvalid   Code = "BLOB_UPLOAD_INVALID"
	CodeBlobUploadUnknown   Code = "BLOB_UPLOAD_UNKNOWN"
	CodeDigestInvalid       Code = "DIGEST_INVALID"
	CodeManifestBlobUnknown Code = "MANIFEST_BLOB_UNKNOWN"
	CodeManifestInvalid     Code = "MANIFEST_INVALID"
	CodeManifestUnknown     Code = "MANIFEST_UNKNOWN"
	CodeNameInvalid         Code = "NAME_INVALID"
	CodeNameUnknown         Code = "NAME_UNKNOWN"
	CodeSizeInvalid         Code = "SIZE_INVALID"
	CodeTagInvalid          Code = "TAG_INVALID"
	CodeUnauthorized        Code = "UNAUTHORIZED"
	CodeDenied              Code = "DENIED"
	CodeUnsupported         Code = "UNSUPPORTED"
	CodeTooManyRequests     Code = "TOOMANYREQUESTS"

	// AccelRegistry-specific codes (namespaced with ACCEL_)
	CodeAccelNotFound      Code = "ACCEL_NOT_FOUND"
	CodeAccelInvalidType   Code = "ACCEL_INVALID_TYPE"
	CodeAccelSourceMissing Code = "ACCEL_SOURCE_MISSING"
	CodeAccelIndexCorrupt  Code = "ACCEL_INDEX_CORRUPT"
	CodeDAGIncomplete      Code = "DAG_INCOMPLETE"
)

// ────────────────────────────────────────────────────────────────────────────
// RegistryError — base error type
// ────────────────────────────────────────────────────────────────────────────

// RegistryError wraps an OCI-spec error code with an HTTP status code,
// a human-readable message, and an optional cause.
type RegistryError struct {
	Code       Code
	HTTPStatus int
	Message    string
	Detail     any
	Cause      error
}

func (e *RegistryError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *RegistryError) Unwrap() error { return e.Cause }

// Is satisfies errors.Is by matching on Code.
func (e *RegistryError) Is(target error) bool {
	t, ok := target.(*RegistryError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// ────────────────────────────────────────────────────────────────────────────
// Constructors
// ────────────────────────────────────────────────────────────────────────────

// New constructs a RegistryError without a cause.
func New(code Code, status int, msg string) *RegistryError {
	return &RegistryError{Code: code, HTTPStatus: status, Message: msg}
}

// Wrap constructs a RegistryError that wraps cause.
func Wrap(code Code, status int, msg string, cause error) *RegistryError {
	return &RegistryError{Code: code, HTTPStatus: status, Message: msg, Cause: cause}
}

// WithDetail attaches a detail payload (returned in the OCI error JSON body).
func (e *RegistryError) WithDetail(detail any) *RegistryError {
	cp := *e
	cp.Detail = detail
	return &cp
}

// ────────────────────────────────────────────────────────────────────────────
// Sentinel errors — use errors.Is() to test
// ────────────────────────────────────────────────────────────────────────────

var (
	ErrBlobUnknown      = New(CodeBlobUnknown, http.StatusNotFound, "blob unknown to registry")
	ErrManifestUnknown  = New(CodeManifestUnknown, http.StatusNotFound, "manifest unknown")
	ErrNameUnknown      = New(CodeNameUnknown, http.StatusNotFound, "repository name not known to registry")
	ErrDigestInvalid    = New(CodeDigestInvalid, http.StatusBadRequest, "provided digest did not match uploaded content")
	ErrManifestInvalid  = New(CodeManifestInvalid, http.StatusBadRequest, "manifest invalid")
	ErrUnauthorized     = New(CodeUnauthorized, http.StatusUnauthorized, "authentication required")
	ErrDenied           = New(CodeDenied, http.StatusForbidden, "requested access to the resource is denied")
	ErrUnsupported      = New(CodeUnsupported, http.StatusMethodNotAllowed, "the operation is unsupported")
	ErrAccelNotFound    = New(CodeAccelNotFound, http.StatusNotFound, "no accelerated variant found for source digest")
	ErrAccelInvalidType = New(CodeAccelInvalidType, http.StatusBadRequest, "unknown or unsupported acceleration type")
	ErrDAGIncomplete    = New(CodeDAGIncomplete, http.StatusNotFound, "DAG contains missing nodes")
)

// ────────────────────────────────────────────────────────────────────────────
// Typed helper constructors
// ────────────────────────────────────────────────────────────────────────────

// BlobUnknown constructs a BLOB_UNKNOWN error for a specific digest.
func BlobUnknown(dgst digest.Digest) *RegistryError {
	return ErrBlobUnknown.WithDetail(map[string]string{"digest": dgst.String()})
}

// ManifestUnknown constructs a MANIFEST_UNKNOWN error for a reference.
func ManifestUnknown(ref string) *RegistryError {
	return ErrManifestUnknown.WithDetail(map[string]string{"reference": ref})
}

// AccelNotFound constructs an ACCEL_NOT_FOUND error for a source digest.
func AccelNotFound(sourceDigest digest.Digest) *RegistryError {
	return ErrAccelNotFound.WithDetail(map[string]string{
		"sourceDigest": sourceDigest.String(),
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// HTTPStatus extracts the HTTP status from err.
// Returns http.StatusInternalServerError if err is not a *RegistryError.
func HTTPStatus(err error) int {
	var re *RegistryError
	if errors.As(err, &re) {
		return re.HTTPStatus
	}
	return http.StatusInternalServerError
}

// IsNotFound reports whether err indicates a 404-class error.
func IsNotFound(err error) bool {
	return HTTPStatus(err) == http.StatusNotFound
}

// IsUnauthorized reports whether err indicates an auth error.
func IsUnauthorized(err error) bool {
	var re *RegistryError
	return errors.As(err, &re) && re.HTTPStatus == http.StatusUnauthorized
}

// Additional sentinel errors
var (
	ErrBlobUploadUnknown = New(CodeBlobUploadUnknown, http.StatusNotFound, "blob upload unknown")
	ErrNameInvalid       = New(CodeNameInvalid, http.StatusBadRequest, "invalid repository name")
)
