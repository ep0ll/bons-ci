package registry

import (
	"errors"
	"io"
)

var (
	// Store errors
	ErrInvalidReference  = errors.New("invalid reference")
	ErrMissingDescriptor = errors.New("missing descriptor")
	ErrNotFound          = errors.New("content not found")
	ErrNotSupported      = errors.New("operation not supported by registry")

	// Ingestion errors
	ErrNoActiveIngestion  = errors.New("no active ingestion found")
	ErrDupActiveIngestion = errors.New("an active ingestion already exists with the given ref")
	ErrRequiredReference  = errors.New("no reference provided to filter ingestions")
	ErrInvalidFilter      = errors.New("invalid filter format, expected key==value")

	// Reader errors
	ErrNilReader        = io.ErrUnexpectedEOF
	ErrNilWriter        = io.ErrUnexpectedEOF
	ErrSeekNotSupported = io.ErrNoProgress

	// Repo (Registry cache) errors
	ErrInvalidRegistryRef     = errors.New("registry reference is invalid")
	ErrRegistryRefExists      = errors.New("registry reference already exists")
	ErrInvalidRegistry        = errors.New("invalid oci registry")
	ErrRegistryNotFound       = errors.New("registry not found")
	ErrRegistryCreationFailed = errors.New("failed to create registry")
)
