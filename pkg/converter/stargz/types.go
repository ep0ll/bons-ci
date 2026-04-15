package stargz

import (
	"context"

	"github.com/bons/bons-ci/core/images/converter"
)

const (
	LayerAnnotationUncompressed = "containerd.io/uncompressed"
	LayerAnnotationTOCDigest    = "containerd.io/snapshot/stargz/toc.digest"
	LayerAnnotationStargz       = "containerd.io/snapshot/stargz"

	configGCLabelKey = "containerd.io/gc.ref.content.config"
)

// PackOption holds the configurations for stargz layer conversion.
type PackOption struct {
	ChunkSize        int
	MinChunkSize     int
	CompressionLevel int // Typically gzip.BestCompression
}

// LayerConvertHook aligns with the reactive ingestion pipeline event hooks.
//
//   - LayerReader is invoked synchronously prior to standard OCI to Stargz Build conversion.
//   - Done gives a synchronous callback when blob processing is completed or fails.
type LayerConvertHook struct {
	LayerReader converter.LayerReaderFunc
	Done        func(ctx context.Context)
}

// MergeOption defines options for manifest conversion hooks.
type MergeOption struct {
	OCI           bool
	MergeManifest bool
}
