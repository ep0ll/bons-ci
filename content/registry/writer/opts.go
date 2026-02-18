package writer

import (
	"github.com/bons/bons-ci/content/registry/ingestion"
	"github.com/distribution/reference"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type opt struct {
	ref  string
	desc ocispecs.Descriptor

	ingestManager ingestion.IngestManager
}

type Opts func(*opt) error

func WithReference(ref string) Opts {
	return func(o *opt) error {
		if _, err := reference.Parse(ref); err != nil {
			return err
		}

		o.ref = ref
		return nil
	}
}

func WithDescriptor(desc ocispecs.Descriptor) Opts {
	return func(o *opt) (err error) {
		if desc.Digest != "" {
			err = desc.Digest.Validate()
		}

		o.desc = desc
		return err
	}
}

func WithIngestManager(m ingestion.IngestManager) Opts {
	return func(o *opt) error {
		if m == nil {
			return errors.New("unable to resolve Ingestion Manager")
		}

		o.ingestManager = m
		return nil
	}
}
