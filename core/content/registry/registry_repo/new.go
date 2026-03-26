package registry_repo

import "github.com/containerd/containerd/v2/core/transfer/registry"

func NewOCIRegistryRepo() RegistryRepo {
	return &registryRepo{
		registries: make(map[string]*registry.OCIRegistry),
	}
}
