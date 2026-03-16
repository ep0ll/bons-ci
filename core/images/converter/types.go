package converter

import (
	"context"
	"encoding/json"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images/converter"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	ConvertFunc     = converter.ConvertFunc
	ConvertHookFunc = converter.ConvertHookFunc

	// contentSectionReader is an alias for io.SectionReader that makes
	// LayerReaderFunc signatures self-documenting.
	ContentSectionReader = io.SectionReader

	// LayerReaderFunc is invoked synchronously when a layer's raw bytes become
	// available during conversion.
	//
	// Synchronous contract: LayerReaderFunc is called on the same goroutine that
	// owns the conversion (not fire-and-forget). This guarantees that any pipeline
	// event sent inside the function completes before layerConvertFunc returns, so
	// the caller can safely close the events channel once all conversions finish.
	LayerReaderFunc func(ctx context.Context, ra *ContentSectionReader, cs content.Store, desc ocispec.Descriptor) error
)

type ConvertHooks struct {
	// PostConvertHook is a callback function called for each blob after conversion is done.
	PostConvertHook ConvertHookFunc
	// PreConvertHook is a callback function called for each blob before conversion starts.
	PreConvertHook ConvertFunc
}

// DualConfig covers Docker config (v1.0, v1.1, v1.2) and OCI config.
// Unmarshalled as map[string]*json.RawMessage to retain unknown fields on remarshalling.
type DualConfig map[string]*json.RawMessage
