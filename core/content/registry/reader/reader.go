package reader

import (
	"context"
	"io"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// Errors are defined as constants for easier checking by callers.
var (
	ErrNilReader        = errors.New("reader: underlying ReadCloser is nil")
	ErrNilWriter        = errors.New("reader: underlying content.Writer is nil")
	ErrSeekNotSupported = errors.New("reader: random access (Seek) not supported by underlying stream")
)

type Reader interface {
	content.ReaderAt
	Reader() io.Reader
}

type FetcherAt interface {
	FetchAt(context.Context, ocispec.Descriptor) (Reader, error)
}

func RegistryReader(r io.ReadCloser, w content.Writer, size int64) (Reader, error) {
	if r == nil {
		return nil, ErrNilReader
	}

	if w == nil {
		return nil, ErrNilWriter
	}

	return &registryReader{
		tee: io.TeeReader(r, w),
		readSeekerOnce: sync.OnceValues(func() (io.ReadSeekCloser, error) {
			sr, ok := r.(io.ReadSeekCloser)
			if !ok {
				return struct {
					io.ReadCloser
					io.Seeker
				}{
					ReadCloser: r,
				}, ErrSeekNotSupported
			}
			return sr, nil
		}),
		size: size,
	}, nil
}

type registryReader struct {
	tee            io.Reader
	size           int64
	readSeekerOnce func() (io.ReadSeekCloser, error)
	mu             sync.Mutex
}

// Close implements Reader.
func (r *registryReader) Close() error {
	if clsr, ok := r.tee.(io.Closer); ok {
		return clsr.Close()
	}

	reader, _ := r.readSeekerOnce()
	return reader.Close()
}

// ReadAt implements Reader.
func (r *registryReader) ReadAt(p []byte, off int64) (n int, err error) {
	reader, err := r.readSeekerOnce()
	if err != nil {
		return -1, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if n, err := reader.Seek(off, io.SeekStart); err != nil {
		return int(n), err
	}

	return reader.Read(p)
}

func (r *registryReader) Read(p []byte) (n int, err error) {
	return r.tee.Read(p)
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
