package ingestion

import "github.com/pkg/errors"

var (
	ErrNoActiveIngestion  = errors.Errorf("no active ingestion found")
	ErrDupActiveIngestion = errors.Errorf("an active ingestion found with the given ref")
	ErrRequiredReference  = errors.Errorf("no refernce provided to filter ingestions")
)
