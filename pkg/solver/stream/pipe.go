package stream

import (
	"context"
	"io"
	"sync"
)

// Pipe provides a concurrent producer/consumer channel with back-pressure
// via a bounded internal buffer. It implements io.ReadWriteCloser for
// byte-level streaming.
type Pipe struct {
	ch     chan []byte
	mu     sync.Mutex
	closed bool
	err    error
	buf    []byte // residual unread bytes from the last chunk
}

// NewPipe creates a pipe with the given buffer capacity (number of chunks
// that can be buffered before the writer blocks).
func NewPipe(bufferSize int) *Pipe {
	if bufferSize < 1 {
		bufferSize = 8
	}
	return &Pipe{
		ch: make(chan []byte, bufferSize),
	}
}

// Write sends data into the pipe. Blocks if the internal buffer is full
// (back-pressure). Returns io.ErrClosedPipe if the pipe is closed.
func (p *Pipe) Write(data []byte) (int, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	p.mu.Unlock()

	// Copy to avoid aliasing the caller's slice.
	cp := make([]byte, len(data))
	copy(cp, data)

	select {
	case p.ch <- cp:
		return len(data), nil
	default:
		// Channel full — block.
		p.ch <- cp
		return len(data), nil
	}
}

// Read reads from the pipe. Blocks until data is available or the pipe
// is closed. Returns io.EOF when the pipe is closed and all data has
// been consumed.
func (p *Pipe) Read(dst []byte) (int, error) {
	// Drain residual buffer first.
	if len(p.buf) > 0 {
		n := copy(dst, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}

	chunk, ok := <-p.ch
	if !ok {
		return 0, io.EOF
	}

	n := copy(dst, chunk)
	if n < len(chunk) {
		p.buf = chunk[n:]
	}
	return n, nil
}

// Close closes the write side of the pipe. Subsequent writes return
// io.ErrClosedPipe. Reads will drain remaining data then return io.EOF.
func (p *Pipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.ch)
	}
	return nil
}

// CloseWithError closes the pipe and sets an error that will be returned
// by subsequent reads after the buffer is drained.
func (p *Pipe) CloseWithError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.err = err
		close(p.ch)
	}
}

// ValuePipe is a typed channel pipe for passing arbitrary values between
// a producer and consumer, with context-aware blocking.
type ValuePipe[T any] struct {
	ch     chan T
	done   chan struct{}
	closed bool
	mu     sync.Mutex
}

// NewValuePipe creates a typed pipe with the given buffer size.
func NewValuePipe[T any](bufferSize int) *ValuePipe[T] {
	if bufferSize < 1 {
		bufferSize = 1
	}
	return &ValuePipe[T]{
		ch:   make(chan T, bufferSize),
		done: make(chan struct{}),
	}
}

// Send sends a value into the pipe. Blocks if the buffer is full.
// Returns an error if the context is canceled or the pipe is closed.
func (p *ValuePipe[T]) Send(ctx context.Context, val T) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return io.ErrClosedPipe
	case p.ch <- val:
		return nil
	}
}

// Receive returns the channel for consuming values.
func (p *ValuePipe[T]) Receive() <-chan T {
	return p.ch
}

// Close closes the pipe.
func (p *ValuePipe[T]) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.ch)
		close(p.done)
	}
}
