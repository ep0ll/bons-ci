package content

import (
	"github.com/containerd/containerd/v2/core/content"
)

func NewLocalFirstStore(local, registry content.Store) content.Store {
	return &localFirstContentStore{local: local, registry: registry}
}

func NewMultiContentStore(store ...content.Store) content.Store {
	return &multiContentStore{stores: store}
}
