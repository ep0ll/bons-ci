package cache

import "github.com/containerd/containerd/v2/"

type Entry interface {
	Metadata() (metadata, error)
	Mount() (images.Store, error)
}

type metadata struct {
	Size int64
	ID string
}
