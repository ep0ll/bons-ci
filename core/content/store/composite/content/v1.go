package content

import (
	"context"

	"github.com/bons/bons-ci/core/content/store/composite/content/util"
	"github.com/bons/bons-ci/core/content/store/composite/content/writer"
	"github.com/containerd/containerd/content"
	v2content "github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func V1ContentStore(store v2content.Store) content.Store {
	return &v1ContentStore{store: store}
}

type v1ContentStore struct {
	store v2content.Store
}

// Abort implements content.Store.
func (v *v1ContentStore) Abort(ctx context.Context, ref string) error {
	return v.store.Abort(ctx, ref)
}

// Delete implements content.Store.
func (v *v1ContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return v.store.Delete(ctx, dgst)
}

// Info implements content.Store.
func (v *v1ContentStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	info, err := v.store.Info(ctx, dgst)
	return content.Info(info), err
}

// ListStatuses implements content.Store.
func (v *v1ContentStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	statuses, err := v.store.ListStatuses(ctx, filters...)
	return util.V1Statuses(statuses...), err
}

// ReaderAt implements content.Store.
func (v *v1ContentStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	return v.store.ReaderAt(ctx, desc)
}

// Status implements content.Store.
func (v *v1ContentStore) Status(ctx context.Context, ref string) (content.Status, error) {
	st, err := v.store.Status(ctx, ref)
	return content.Status(st), err
}

// Update implements content.Store.
func (v *v1ContentStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	i, err := v.store.Update(ctx, v2content.Info(info))
	return content.Info(i), err
}

// Walk implements content.Store.
func (v *v1ContentStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return v.store.Walk(ctx, util.V2WalkFunc(fn), filters...)
}

// Writer implements content.Store.
func (v *v1ContentStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	w, err := v.store.Writer(ctx, util.V2ContentWriterOpt(opts...)...)
	return writer.V1Writer(w), err
}

var _ content.Store = (*v1ContentStore)(nil)
