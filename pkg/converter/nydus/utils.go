package nydus

import (
	"fmt"
	"io"
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

type writeCloser struct {
	closed bool
	io.WriteCloser
	action func() error
}

func (c *writeCloser) Close() error {
	if c.closed {
		return nil
	}

	if err := c.WriteCloser.Close(); err != nil {
		return err
	}
	c.closed = true

	if err := c.action(); err != nil {
		return err
	}

	return nil
}

func newWriteCloser(wc io.WriteCloser, action func() error) io.WriteCloser {
	return &writeCloser{
		WriteCloser: wc,
		action:      action,
	}
}
