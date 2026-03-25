package executor

import (
	"context"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/moby/sys/user"
)

type Mount struct {
	Src      Mountable
	Selector string
	Dest     string
	Readonly bool
}

type MountableRef interface {
	Mount() ([]mount.Mount, func() error, error)
	IdentityMapping() *user.IdentityMapping
}

type Mountable interface {
	Mount(ctx context.Context, readonly bool) (MountableRef, error)
}

