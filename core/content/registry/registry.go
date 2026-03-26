package registry

import (
	"context"

	"github.com/containerd/containerd/v2/core/transfer/registry"
)

// getOrCreateRegistry returns a cached registry instance or creates a new one
func GetOrCreateRegistry(ctx context.Context, ref string, r *registryStore) (*registry.OCIRegistry, error) {
	reg, err := r.registryCache.Get(ctx, ref)
	if err == nil {
		return reg, nil
	}

	return r.registryCache.Put(ctx, ref, r.opts...)
}
