package writer

import (
	"context"
	"errors"

	"github.com/bons/bons-ci/core/content/registry/ingestion"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

type RegistryWriter interface {
	ingestion.ActiveIngestion
}

type registryWriter struct {
	ingestion.ActiveIngestion
	ctx context.Context
	opt *opt
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

func NewRegistryWriter(ctx context.Context, writer content.Writer, opts ...Opts) (_ RegistryWriter, err error) {
	var opt = &opt{}
	for _, op := range opts {
		if err := op(opt); err != nil {
			return nil, err
		}
	}

	return &registryWriter{
		ActiveIngestion: ingestion.Ingestion(writer, opt.ref, opt.desc),
		ctx:             ctx,
		opt:             opt,
	}, nil
}
