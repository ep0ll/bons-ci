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

func V2ContentStore(store content.Store) v2content.Store {
	return &v2ContentStore{store: store}
}

type v2ContentStore struct {
	store content.Store
}

// Abort implements content.Store.
func (v *v2ContentStore) Abort(ctx context.Context, ref string) error {
	return v.store.Abort(ctx, ref)
}

// Delete implements content.Store.
func (v *v2ContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return v.store.Delete(ctx, dgst)
}

// Info implements content.Store.
func (v *v2ContentStore) Info(ctx context.Context, dgst digest.Digest) (v2content.Info, error) {
	info, err := v.store.Info(ctx, dgst)
	return v2content.Info(info), err
}

// ListStatuses implements content.Store.
func (v *v2ContentStore) ListStatuses(ctx context.Context, filters ...string) ([]v2content.Status, error) {
	statuses, err := v.store.ListStatuses(ctx, filters...)
	return util.V2Statuses(statuses...), err
}

// ReaderAt implements content.Store.
func (v *v2ContentStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (v2content.ReaderAt, error) {
	return v.store.ReaderAt(ctx, desc)
}

// Status implements content.Store.
func (v *v2ContentStore) Status(ctx context.Context, ref string) (v2content.Status, error) {
	st, err := v.store.Status(ctx, ref)
	return v2content.Status(st), err
}

// Update implements content.Store.
func (v *v2ContentStore) Update(ctx context.Context, info v2content.Info, fieldpaths ...string) (v2content.Info, error) {
	i, err := v.store.Update(ctx, content.Info(info))
	return v2content.Info(i), err
}

// Walk implements content.Store.
func (v *v2ContentStore) Walk(ctx context.Context, fn v2content.WalkFunc, filters ...string) error {
	return v.store.Walk(ctx, util.V1WalkFunc(fn), filters...)
}

// Writer implements content.Store.
func (v *v2ContentStore) Writer(ctx context.Context, opts ...v2content.WriterOpt) (v2content.Writer, error) {
	w, err := v.store.Writer(ctx, util.V1ContentWriterOpt(opts...)...)
	return writer.V2Writer(w), err
}

var _ v2content.Store = (*v2ContentStore)(nil)
