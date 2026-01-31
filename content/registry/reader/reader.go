package reader

import (
	"context"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type Reader interface {
	content.ReaderAt
	Reader() io.Reader
}

type FetcherAt interface {
	FetchAt(context.Context, ocispec.Descriptor) (Reader, error)
}

func RegistryReader(r io.ReadCloser, w content.Writer, size int64) (Reader, error) {
	return &registryReader{
		fetcher: r,
		store:   w,
		size:    size,
	}, nil
}

type registryReader struct {
	fetcher io.ReadCloser
	size    int64
	store   content.Writer
}

// Close implements Reader.
func (r *registryReader) Close() error {
	if err := r.store.Commit(context.Background(), 0, ""); err != nil {
		return err
	}
	return r.fetcher.Close()
}

// ReadAt implements Reader.
func (r *registryReader) ReadAt(p []byte, off int64) (n int, err error) {
	if reader, ok := r.fetcher.(interface {
		Seek(offset int64, whence int) (int64, error)
		io.Reader
	}); ok {
		if n, err := reader.Seek(off, io.SeekStart); err != nil {
			return int(n), err
		}

		return reader.Read(p)
	}

	return int(off), fmt.Errorf("Reader.Seek unsupported")
}

func (r *registryReader) Read(p []byte) (n int, err error) {
	return io.TeeReader(r.fetcher, r.store).Read(p)
}

// Reader implements Reader.
func (r *registryReader) Reader() io.Reader {
	return r
}

// Size implements Reader.
func (r *registryReader) Size() int64 {
	return r.size
}

var _ Reader = &registryReader{}
