package nydus

import (
	"github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/containerd/nydus-snapshotter/snapshot"
)

var NewSnapshotter = snapshot.NewSnapshotter

var _ = converter.Unpack