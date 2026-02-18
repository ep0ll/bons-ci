package noop

import "github.com/containerd/containerd/v2/core/content"

type readerAt struct{}

var _ content.ReaderAt = &readerAt{}

// Close implements content.ReaderAt.
func (r *readerAt) Close() error {
	return nil
}

// ReadAt implements content.ReaderAt.
func (r *readerAt) ReadAt(p []byte, off int64) (n int, err error) {
	return len(p), nil
}

// Size implements content.ReaderAt.
func (r *readerAt) Size() int64 {
	return -1
}

func NoopReaderAt() content.ReaderAt {
	return &readerAt{}
}
