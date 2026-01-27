package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/bons/bons-ci/content/registry/reader"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type registryStore struct {
	ref   string
	registry *registry.OCIRegistry
	store content.Store
	opts []registry.Opt
}

// Abort implements ContentStore.
func (r *registryStore) Abort(ctx context.Context, ref string) error {
	return r.store.Abort(ctx, ref)
}

// Delete implements ContentStore.
func (r *registryStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return r.store.Delete(ctx, dgst)
}

// Info implements ContentStore.
func (r *registryStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if i, err := r.store.Info(ctx, dgst); err == nil {
		return i, nil
	}

	ref, err := reference.ParseNamed(r.ref)
	if err != nil {
		return content.Info{}, err
	}

	dref, err := reference.WithDigest(ref, dgst)
	if err != nil {
		return content.Info{}, err
	}
	reg, err := registry.NewOCIRegistry(ctx, dref.String(), r.opts...)
	if err != nil {
		return content.Info{}, err
	}

	_, desc, err := reg.Resolve(ctx)
	return content.Info{
		Digest: desc.Digest,
		Size: desc.Size,
		Labels: desc.Annotations,	
	}, err
}

// ListStatuses implements ContentStore.
func (r *registryStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return r.store.ListStatuses(ctx, filters...)
}

// ReaderAt implements ContentStore.
func (r *registryStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	fetcher, err := r.registry.Fetcher(ctx, r.ref)
	if err != nil {
		return nil, err
	}

	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}

	writer, err := r.store.Writer(ctx, content.WithDescriptor(desc))
	if err != nil {
		return nil, err
	}

	return reader.RegistryReader(rc, writer, desc.Size)
}

// Status implements ContentStore.
func (r *registryStore) Status(ctx context.Context, ref string) (content.Status, error) {
	return r.store.Status(ctx, ref)
}

// Update implements ContentStore.
func (r *registryStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return r.store.Update(ctx, info, fieldpaths...)
}

// Walk implements ContentStore.
func (r *registryStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return r.store.Walk(ctx, fn, filters...)
}

// Writer implements ContentStore.
func (r *registryStore) Writer(ctx context.Context, opts ...content.WriterOpt) (_ content.Writer, err error) {
	var opt = &content.WriterOpts{}
	for _, op := range opts {
		if err := op(opt); err != nil {
			return nil, err
		}
	}

	if d := opt.Desc.Digest.String(); d == "" {
		if !strings.Contains(opt.Ref, "@") {
			return nil, fmt.Errorf("WithDescriptor opt is required")
		}
		opt.Desc.Digest, err = digest.Parse(opt.Ref)
		if err != nil {
			return nil, err
		}
	}

	pusher, err := r.registry.Pusher(ctx, opt.Desc)
	if err != nil {
		return nil, err
	}

	return pusher.Push(ctx, opt.Desc)
}
