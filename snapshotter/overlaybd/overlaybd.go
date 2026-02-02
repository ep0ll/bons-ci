//go:build linux

package overlaybd

import (
	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/containerd/accelerated-container-image/pkg/convertor"
)

var NewSnapshotter = snapshot.NewSnapshotter
var NewOverlaybdConvertor = convertor.NewOverlaybdConvertor