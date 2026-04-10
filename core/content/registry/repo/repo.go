package registry_repo

import (
	"context"
	"errors"

	"github.com/containerd/containerd/v2/core/transfer/registry"
)

type RegistryRepo interface {
	Get(ctx context.Context, ref string) (*registry.OCIRegistry, error)
	Put(ctx context.Context, ref string, opts ...registry.Opt) (*registry.OCIRegistry, error)
	Exists(ctx context.Context, ref string) (bool, error)
}

var _ RegistryRepo = &registryRepo{}

type registryRepo struct {
	registries map[string]*registry.OCIRegistry
}

// Exists implements RegistryRepo.
func (r *registryRepo) Exists(_ context.Context, ref string) (bool, error) {
	reg, ok := r.registries[ref]
	if reg == nil {
		return ok, ErrInvalidRegistry
	}

	return ok, nil
}

// Get implements RegistryRepo.
func (r *registryRepo) Get(_ context.Context, ref string) (*registry.OCIRegistry, error) {
	reg, ok := r.registries[ref]
	if !ok {
		return nil, ErrRegistryNotFound
	}

	if reg == nil {
		return nil, ErrInvalidRegistry
	}

	return reg, nil
}

// Put implements RegistryRepo.
func (r *registryRepo) Put(ctx context.Context, ref string, opts ...registry.Opt) (*registry.OCIRegistry, error) {
	if ref == "" {
		return nil, ErrInvalidRegistryRef
	}

	if _, ok := r.registries[ref]; ok {
		return nil, ErrRegistryRefExists
	}

	reg, err := registry.NewOCIRegistry(ctx, ref, opts...)
	if err != nil {
		return nil, errors.Join(err, ErrRegistryCreationFailed)
	}

	if reg == nil {
		return nil, ErrInvalidRegistry
	}

	r.registries[ref] = reg
	return reg, nil
}
