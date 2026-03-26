package registry

import (
	"context"
	"time"

	"github.com/bons/bons-ci/core/content/registry/ingestion"
	"github.com/bons/bons-ci/core/content/registry/registry_repo"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/distribution/reference"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// NewStore initializes a new registry-backed content store.
// It uses a local content store for caching and downloaded content.
// Active ingestions are tracked in memory via an IngestManager.
func NewStore(ref, root string, opts ...registry.Opt) (content.Store, error) {
	if ref == "" {
		return nil, ErrInvalidReference
	}

	// Validate reference format
	if _, err := reference.ParseNamed(ref); err != nil {
		return nil, ErrInvalidReference
	}

	st, err := local.NewStore(root)
	if err != nil {
		return nil, err
	}

	repo := registry_repo.NewOCIRegistryRepo()
	_, err = repo.Put(context.Background(), ref, opts...)
	if err != nil {
		return nil, err
	}

	return &registryStore{
		ref:           ref,
		store:         st,
		opts:          opts,
		infoCacheTTL:  5 * time.Minute,
		registryCache: repo,
		ingester:      ingestion.NewIngestManager(),
	}, nil
}

func Fetcher(ctx context.Context, ref string, repo registry_repo.RegistryRepo) (transfer.Fetcher, error) {
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

func Pusher(ctx context.Context, ref string, desc v1.Descriptor, repo registry_repo.RegistryRepo) (transfer.Pusher, error) {
	reg, err := repo.Get(ctx, ref)
	if err != nil {
		return nil, err
	}

	return reg.Pusher(ctx, desc)
}
