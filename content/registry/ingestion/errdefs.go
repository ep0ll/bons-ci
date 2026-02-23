package ingestion

import "errors"

var (
	// ErrNoActiveIngestion is returned when no active ingestion is found for the given ref.
	ErrNoActiveIngestion = errors.New("no active ingestion found")
	// ErrDupActiveIngestion is returned when attempting to register an ingestion
	// with a ref that already has an active ingestion.
	ErrDupActiveIngestion = errors.New("an active ingestion already exists with the given ref")
	// ErrRequiredReference is returned when a filter requires a reference but none is provided.
	ErrRequiredReference = errors.New("no reference provided to filter ingestions")
	// ErrInvalidFilter is returned when a filter string has an unsupported format.
	ErrInvalidFilter = errors.New("invalid filter format, expected key==value")
)
