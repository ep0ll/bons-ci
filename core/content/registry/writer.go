package registry

import (
	"context"
	"errors"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type RegistryWriter interface {
	ActiveIngestion
}

type writerOpt struct {
	ref           string
	desc          ocispecs.Descriptor
	ingestManager IngestManager
}

type WriterOpts func(*writerOpt) error

// withWriterReference sets the ingestion reference string.
// This is used as the key for tracking active ingestions.
func withWriterReference(ref string) WriterOpts {
	return func(o *writerOpt) error {
		if ref == "" {
			return ErrRequiredReference
		}
		o.ref = ref
		return nil
	}
}

// withWriterDescriptor sets the OCI descriptor for the content being written.
func withWriterDescriptor(desc ocispecs.Descriptor) WriterOpts {
	return func(o *writerOpt) (err error) {
		if desc.Digest != "" {
			err = desc.Digest.Validate()
		}
		o.desc = desc
		return err
	}
}

// withIngestManager sets the IngestManager for tracking active ingestions.
// The writer will register itself on creation and deregister on commit/close.
func withIngestManager(m IngestManager) WriterOpts {
	return func(o *writerOpt) error {
		if m == nil {
			return ErrNoActiveIngestion
		}
		o.ingestManager = m
		return nil
	}
}

type registryWriter struct {
	ActiveIngestion
	ctx context.Context
	opt *writerOpt
}

// Close implements RegistryWriter.
func (r *registryWriter) Close() (err error) {
	ref, _ := r.ActiveIngestion.ID()
	rerr := r.ActiveIngestion.Close()
	if rerr == nil {
		if im := r.opt.ingestManager; im != nil {
			_, err = im.Delete(r.ctx, ref)
		}
	}

	return errors.Join(rerr, err)
}

// Commit implements RegistryWriter.
func (r *registryWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	ref, _ := r.ActiveIngestion.ID()
	err := r.ActiveIngestion.Commit(ctx, size, expected, opts...)
	if err != nil {
		return err
	}

	if im := r.opt.ingestManager; im != nil {
		_, err = im.Delete(ctx, ref)
	}

	return err
}

// Digest implements RegistryWriter.
func (r *registryWriter) Digest() digest.Digest {
	return r.ActiveIngestion.Digest()
}

// Status implements RegistryWriter.
func (r *registryWriter) Status() (content.Status, error) {
	return r.ActiveIngestion.Status()
}

// Truncate implements RegistryWriter.
func (r *registryWriter) Truncate(size int64) error {
	return r.ActiveIngestion.Truncate(size)
}

// Write implements RegistryWriter.
func (r *registryWriter) Write(p []byte) (n int, err error) {
	return r.ActiveIngestion.Write(p)
}

var _ RegistryWriter = &registryWriter{}

func newRegistryWriter(ctx context.Context, writer content.Writer, opts ...WriterOpts) (RegistryWriter, error) {
	var opt = &writerOpt{}
	for _, op := range opts {
		if err := op(opt); err != nil {
			return nil, err
		}
	}

	return &registryWriter{
		ActiveIngestion: newActiveIngestion(writer, opt.ref, opt.desc),
		ctx:             ctx,
		opt:             opt,
	}, nil
}
