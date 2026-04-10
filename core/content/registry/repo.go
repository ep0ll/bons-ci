package registry

import (
	"context"
	"sync"

	"github.com/containerd/containerd/v2/core/transfer/registry"
)

// RegistryRepo manages a thread-safe cache of OCI registry instances.
type RegistryRepo interface {
	Get(ctx context.Context, ref string) (*registry.OCIRegistry, error)
	Put(ctx context.Context, ref string, opts ...registry.Opt) (*registry.OCIRegistry, error)
	Exists(ctx context.Context, ref string) (bool, error)
}

var _ RegistryRepo = &registryRepo{}

type registryRepo struct {
	mu         sync.RWMutex
	registries map[string]*registry.OCIRegistry
}

// newRegistryRepo creates a new registry instance cache.
func newRegistryRepo() RegistryRepo {
	return &registryRepo{
		registries: make(map[string]*registry.OCIRegistry),
	}
}

// Exists implements RegistryRepo.
func (r *registryRepo) Exists(_ context.Context, ref string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	reg, ok := r.registries[ref]
	if !ok {
		return false, nil
	}
	if reg == nil {
		return false, ErrInvalidRegistry
	}

	return true, nil
}

// Get implements RegistryRepo.
func (r *registryRepo) Get(_ context.Context, ref string) (*registry.OCIRegistry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

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

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.registries[ref]; ok {
		return r.registries[ref], nil
	}

	reg, err := registry.NewOCIRegistry(ctx, ref, opts...)
	if err != nil {
		return nil, err
	}

	if reg == nil {
		return nil, ErrInvalidRegistry
	}

	r.registries[ref] = reg
	return reg, nil
}
