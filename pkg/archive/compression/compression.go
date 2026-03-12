package compression

import (
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	gzip "github.com/klauspost/pgzip"
)

// DetectCompression detects the compression algorithm of the source.
func DetectCompression(source []byte) Compression {
	for compression, fn := range map[Compression]matcher{
		Gzip: magicNumberMatcher(gzipMagic),
		Zstd: zstdMatcher(),
	} {
		if fn(source) {
			return compression
		}
	}
	return Uncompressed
}

// DecompressStream decompresses the archive and returns a ReaderCloser with the decompressed archive.
func DecompressStream(archive io.Reader) (DecompressReadCloser, error) {
	buf := newBufferedReader(archive)
	bs, err := buf.Peek(10)
	if err != nil && err != io.EOF {
		// Note: we'll ignore any io.EOF error because there are some odd
		// cases where the layer.tar file will be empty (zero bytes) and
		// that results in an io.EOF from the Peek() call. So, in those
		// cases we'll just treat it as a non-compressed stream and
		// that means just create an empty layer.
		// See Issue docker/docker#18170
		return nil, err
	}

	switch compression := DetectCompression(bs); compression {
	case Uncompressed:
		return &readCloserWrapper{
			Reader:      buf,
			compression: compression,
		}, nil
	case Gzip:
		gzReader, err := gzip.NewReader(buf)
		if err != nil {
			return nil, err
		}

		return &readCloserWrapper{
			Reader:      gzReader,
			compression: compression,
			closer:      gzReader.Close,
		}, nil
	case Zstd:
		zstdReader, err := zstd.NewReader(buf,
			zstd.WithDecoderLowmem(false),
		)
		if err != nil {
			return nil, err
		}
		return &readCloserWrapper{
			Reader:      zstdReader,
			compression: compression,
			closer: func() error {
				zstdReader.Close()
				return nil
			},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported compression format %s", (&compression).Extension())
	}
}
