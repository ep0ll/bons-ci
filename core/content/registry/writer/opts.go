package writer

import (
	"github.com/bons/bons-ci/content/registry/ingestion"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type opt struct {
	ref  string
	desc ocispecs.Descriptor

	ingestManager ingestion.IngestManager
}

type Opts func(*opt) error

// WithReference sets the ingestion reference string.
// This is used as the key for tracking active ingestions.
func WithReference(ref string) Opts {
	return func(o *opt) error {
		if ref == "" {
			return ingestion.ErrRequiredReference
		}
		o.ref = ref
		return nil
	}
}

// WithDescriptor sets the OCI descriptor for the content being written.
func WithDescriptor(desc ocispecs.Descriptor) Opts {
	return func(o *opt) (err error) {
		if desc.Digest != "" {
			err = desc.Digest.Validate()
		}

		o.desc = desc
		return err
	}
}

// WithIngestManager sets the IngestManager for tracking active ingestions.
// The writer will register itself on creation and deregister on commit/close.
func WithIngestManager(m ingestion.IngestManager) Opts {
	return func(o *opt) error {
		if m == nil {
			return ingestion.ErrNoActiveIngestion
		}

		o.ingestManager = m
		return nil
	}
}
