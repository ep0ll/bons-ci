package registry

import "errors"

var (
	// ErrInvalidReference is returned when the reference cannot be parsed
	ErrInvalidReference = errors.New("invalid reference")
	// ErrMissingDescriptor is returned when a descriptor is required but not provided
	ErrMissingDescriptor = errors.New("missing descriptor")
	// ErrNotFound is returned when content is not found in either remote or local store
	ErrNotFound = errors.New("content not found")
	// ErrNotSupported is returned for operations not supported by registries
	ErrNotSupported = errors.New("operation not supported by registry")
)
