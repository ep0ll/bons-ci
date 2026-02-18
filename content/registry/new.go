package registry

import (
	"context"
	"time"

	"github.com/bons/bons-ci/content/registry/registry_repo"
	ocirepo "github.com/bons/bons-ci/content/registry/registry_repo"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/distribution/reference"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func NewStore(ref, root string, opts ...registry.Opt) (content.Store, error) {
	if ref == "" {
		return nil, errors.Wrap(ErrInvalidReference, "reference cannot be empty")
	}

	// Validate reference format
	if _, err := reference.ParseNamed(ref); err != nil {
		return nil, errors.Wrap(ErrInvalidReference, err.Error())
	}

	st, err := local.NewStore(root)
	if err != nil {
		return nil, err
	}

	repo := registry_repo.NewOCIRegistryRepo()
	_, err = repo.Put(context.Background(), ref, opts...)
	return &registryStore{
		ref:           ref,
		store:         st,
		opts:          opts,
		infoCacheTTL:  5 * time.Minute,
		registryCache: repo,
	}, err
}

func Fetcher(ctx context.Context, ref string, repo ocirepo.RegistryRepo) (transfer.Fetcher, error) {
	reg, err := repo.Get(ctx, ref)
	if err != nil {
		return nil, err
	}

	return reg.Fetcher(ctx, ref)
}

func GetOrCreateFetcher(ctx context.Context, r *registryStore, ref string) (transfer.Fetcher, error) {
	reg, err := GetOrCreateRegistry(ctx, ref, r)
	if err != nil {
		return nil, err
	}

	return reg.Fetcher(ctx, ref)
}

func GetOrCreatePusher(ctx context.Context, r *registryStore, ref string, desc v1.Descriptor) (transfer.Pusher, error) {
	reg, err := GetOrCreateRegistry(ctx, ref, r)
	if err != nil {
		return nil, err
	}

	return reg.Pusher(ctx, desc)
}

func Pusher(ctx context.Context, ref string, desc v1.Descriptor, repo ocirepo.RegistryRepo) (transfer.Pusher, error) {
	reg, err := repo.Get(ctx, ref)
	if err != nil {
		return nil, err
	}

	return reg.Pusher(ctx, desc)
}
