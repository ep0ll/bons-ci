package httpapplier

// zstd.go – Thin adapter that wires in zstd decompression.
//
// The klauspost/compress/zstd package provides multi-threaded decompression
// which is significantly faster than a hypothetical single-core fallback.
// It is listed as an OPTIONAL dependency: if the binary is built without it,
// newZstdReader returns an error with a clear message rather than silently
// corrupting data.
//
// To enable zstd: add "github.com/klauspost/compress/zstd" to go.mod and
// replace the stub below with:
//
//	func newZstdReader(src io.Reader) (io.Reader, error) {
//	    d, err := zstd.NewReader(src)
//	    return d, errors.Wrap(err, "zstd decoder")
//	}

import (
	"io"

	"github.com/pkg/errors"
)

// newZstdReader returns a zstd-decompressing reader wrapping src.
// Replace this stub with a real zstd import for production use.
func newZstdReader(_ io.Reader) (io.Reader, error) {
	return nil, errors.New(
		"zstd decompression is not compiled in; " +
			"add github.com/klauspost/compress/zstd to go.mod and " +
			"replace the stub in zstd.go",
	)
}
