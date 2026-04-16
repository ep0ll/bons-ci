package core

import (
	"errors"
	"fmt"
)

// Sentinel errors — use errors.Is for comparison.
var (
	// ErrNotRegistered is returned when an ExporterType is not in the Registry.
	ErrNotRegistered = errors.New("exporter not registered")

	// ErrAlreadyRegistered is returned when the same ExporterType is registered twice.
	ErrAlreadyRegistered = errors.New("exporter already registered")

	// ErrValidation wraps a specific field validation failure.
	ErrValidation = errors.New("validation error")

	// ErrUnsupportedOption is returned when an exporter receives an option key
	// it does not understand and strict-mode is enabled.
	ErrUnsupportedOption = errors.New("unsupported exporter option")

	// ErrTransformFailed wraps a transformer failure with context.
	ErrTransformFailed = errors.New("transform failed")

	// ErrContentNotFound is returned when a requested blob digest is absent.
	ErrContentNotFound = errors.New("content not found")

	// ErrExportAborted indicates the export was cancelled or context expired.
	ErrExportAborted = errors.New("export aborted")
)

// ─── Typed error constructors ──────────────────────────────────────────────

// ValidationError represents a structured field-level validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error: field %q — %s", e.Field, e.Message)
}

func (e *ValidationError) Is(target error) bool {
	return target == ErrValidation
}

// NewValidationError constructs a ValidationError.
func NewValidationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

// OptionError represents an unsupported or malformed option.
type OptionError struct {
	Key     string
	Value   string
	Message string
}

func (e *OptionError) Error() string {
	return fmt.Sprintf("option error: key=%q value=%q — %s", e.Key, e.Value, e.Message)
}

func (e *OptionError) Is(target error) bool {
	return target == ErrUnsupportedOption
}

// NewOptionError constructs an OptionError.
func NewOptionError(key, value, message string) error {
	return &OptionError{Key: key, Value: value, Message: message}
}

// TransformError wraps a transformer failure with the transformer's name.
type TransformError struct {
	TransformerName string
	Cause           error
}

func (e *TransformError) Error() string {
	return fmt.Sprintf("transformer %q failed: %v", e.TransformerName, e.Cause)
}

func (e *TransformError) Unwrap() error { return e.Cause }

func (e *TransformError) Is(target error) bool {
	return target == ErrTransformFailed
}

// NewTransformError wraps cause with the transformer name.
func NewTransformError(name string, cause error) error {
	return &TransformError{TransformerName: name, Cause: cause}
}
