package reader

import (
	"context"
	"io"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Errors for registry reader operations.
var (
	ErrNilReader        = io.ErrUnexpectedEOF
	ErrNilWriter        = io.ErrUnexpectedEOF
	ErrSeekNotSupported = io.ErrNoProgress
)

// Reader extends content.ReaderAt with streaming read capabilities.
type Reader interface {
	content.ReaderAt
	Reader() io.Reader
}

// FetcherAt fetches content as a random-access Reader.
type FetcherAt interface {
	FetchAt(context.Context, ocispec.Descriptor) (Reader, error)
}

// RegistryReader creates a Reader that tees data from a remote ReadCloser
// into a local content.Writer while serving reads. This implements the
// "fetch and cache" pattern used by containerd's registry transfer.
func RegistryReader(r io.ReadCloser, w content.Writer, size int64) (Reader, error) {
	if r == nil {
		return nil, ErrNilReader
	}

	if w == nil {
		return nil, ErrNilWriter
	}

	return &registryReader{
		src:  r,
		tee:  io.TeeReader(r, w),
		size: size,
		readSeekerOnce: sync.OnceValues(func() (io.ReadSeeker, error) {
			sr, ok := r.(io.ReadSeeker)
			if !ok {
				return nil, ErrSeekNotSupported
			}
			return sr, nil
		}),
	}, nil
}

type registryReader struct {
	src            io.ReadCloser
	tee            io.Reader
	size           int64
	readSeekerOnce func() (io.ReadSeeker, error)
	mu             sync.Mutex
}

// Close implements Reader.
func (r *registryReader) Close() error {
	return r.src.Close()
}

// ReadAt implements content.ReaderAt.
// Note: ReadAt requires the underlying stream to support seeking.
// For non-seekable streams, use the Reader() method for sequential access.
func (r *registryReader) ReadAt(p []byte, off int64) (int, error) {
	seeker, err := r.readSeekerOnce()
	if err != nil {
		return 0, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := seeker.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}

	return seeker.Read(p)
}

// Read implements io.Reader via the tee reader (writes through to cache).
func (r *registryReader) Read(p []byte) (int, error) {
	return r.tee.Read(p)
}

// Reader implements Reader, returning a streaming reader that caches to the local store.
func (r *registryReader) Reader() io.Reader {
	return r
}

// Size implements content.ReaderAt.
func (r *registryReader) Size() int64 {
	return r.size
}

var _ Reader = &registryReader{}
