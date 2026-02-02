//go:build linux

package overlayfs

import (
	"github.com/containerd/containerd/v2/plugins/snapshots/overlay"
)

var NewSnapshotter = overlay.NewSnapshotter
