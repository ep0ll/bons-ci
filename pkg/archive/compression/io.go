package compression

import (
	"bufio"
	"io"
)

type readCloserWrapper struct {
	io.Reader
	compression Compression
	closer      func() error
}

func (r *readCloserWrapper) Close() error {
	if r.closer != nil {
		return r.closer()
	}
	return nil
}

func (r *readCloserWrapper) GetCompression() Compression {
	return r.compression
}

type writeCloserWrapper struct {
	io.Writer
	closer func() error
}

func (w *writeCloserWrapper) Close() error {
	if w.closer != nil {
		w.closer()
	}
	return nil
}

type bufferedReader struct {
	buf *bufio.Reader
}

func newBufferedReader(r io.Reader) *bufferedReader {
	buf := bufioReader32KPool.Get().(*bufio.Reader)
	buf.Reset(r)
	return &bufferedReader{buf}
}

func (r *bufferedReader) Read(p []byte) (n int, err error) {
	if r.buf == nil {
		return 0, io.EOF
	}
	n, err = r.buf.Read(p)
	if err == io.EOF {
		r.buf.Reset(nil)
		bufioReader32KPool.Put(r.buf)
		r.buf = nil
	}
	return
}

func (r *bufferedReader) Peek(n int) ([]byte, error) {
	if r.buf == nil {
		return nil, io.EOF
	}
	return r.buf.Peek(n)
}
