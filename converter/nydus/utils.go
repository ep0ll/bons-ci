package nydus

import (
	"archive/tar"
	"fmt"
	"io"
	"path/filepath"

	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	gzip "github.com/klauspost/pgzip"
)

type seekReader struct {
	io.ReaderAt
	pos int64
}

func (ra *seekReader) Read(p []byte) (int, error) {
	n, err := ra.ReadAt(p, ra.pos)
	ra.pos += int64(n)
	return n, err
}

func (ra *seekReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		ra.pos += offset
	case io.SeekStart:
		ra.pos = offset
	default:
		return 0, fmt.Errorf("unsupported whence %d", whence)
	}

	return ra.pos, nil
}

func newSeekReader(ra io.ReaderAt) *seekReader {
	return &seekReader{
		ReaderAt: ra,
		pos:      0,
	}
}

// packToTar packs files to .tar(.gz) stream then return reader.
func packToTar(files []nydusConv.File, compress bool) io.ReadCloser {
	dirHdr := &tar.Header{
		Name:     "image",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}

	pr, pw := io.Pipe()

	go func() {
		// Prepare targz writer
		var tw *tar.Writer
		var gw *gzip.Writer
		var err error

		if compress {
			gw = gzip.NewWriter(pw)
			tw = tar.NewWriter(gw)
		} else {
			tw = tar.NewWriter(pw)
		}

		defer func() {
			err1 := tw.Close()
			var err2 error
			if gw != nil {
				err2 = gw.Close()
			}

			var finalErr error

			// Return the first error encountered to the other end and ignore others.
			switch {
			case err != nil:
				finalErr = err
			case err1 != nil:
				finalErr = err1
			case err2 != nil:
				finalErr = err2
			}

			pw.CloseWithError(finalErr)
		}()

		// Write targz stream
		if err = tw.WriteHeader(dirHdr); err != nil {
			return
		}

		for _, file := range files {
			hdr := tar.Header{
				Name: filepath.Join("image", file.Name),
				Mode: 0444,
				Size: file.Size,
			}
			if err = tw.WriteHeader(&hdr); err != nil {
				return
			}
			if _, err = io.Copy(tw, file.Reader); err != nil {
				return
			}
		}
	}()

	return pr
}
