package registry

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/containerd/containerd/v2/plugins/content/local"
)

func NewStore(ref, root string, opts ...registry.Opt) (content.Store, error) {
	st, err := local.NewStore(root)
	if err != nil {
		return nil, err
	}

	reg, err := registry.NewOCIRegistry(context.Background(), ref, opts...)
	if err != nil {
		return nil, err
	}

	return &registryStore{
		ref:      ref,
		store:    st,
		registry: reg,
	}, nil
}
