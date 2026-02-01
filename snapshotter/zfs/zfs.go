//go:build linux || freebsd

package zfs

import "github.com/containerd/zfs"

var NewSnapshotter = zfs.NewSnapshotter