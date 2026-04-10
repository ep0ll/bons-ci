package b2

import (
	"github.com/containerd/containerd/v2/core/content"
)

// contentReaderAt adapts an ObjectReader to content.ReaderAt.
// It delegates ReadAt, Read, Close to the underlying ObjectReader and
// exposes Size() for range calculations.
type contentReaderAt struct {
	ObjectReader
}

// compile-time check
var _ content.ReaderAt = (*contentReaderAt)(nil)
