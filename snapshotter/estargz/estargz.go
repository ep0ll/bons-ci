//go:build linux

package estargz

import (
	"github.com/containerd/stargz-snapshotter/snapshot"
	"github.com/containerd/stargz-snapshotter/nativeconverter/zstdchunked"
	estargzconverter "github.com/containerd/stargz-snapshotter/nativeconverter/estargz"
	"github.com/containerd/stargz-snapshotter/estargz"
)

var NewSnapshotter = snapshot.NewSnapshotter
var zstd = zstdchunked.LayerConvertWithLayerOptsFuncWithCompressionLevel
var estargzc = estargzconverter.LayerConvertWithLayerAndCommonOptsFunc
var Estargz = estargz.Build
