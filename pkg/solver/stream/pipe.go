package stream

import (
	"context"
	"io"
	"sync"
)

// Pipe provides a concurrent producer/consumer byte channel with back-pressure
// via a bounded internal buffer. It implements io.Reader and io.Writer.
//
// PREVIOUS BUG: the Write method had a dead-branch that caused double-blocking:
//
//	select {
//	case p.ch <- cp:
//	    return len(data), nil  // fast path
//	default:
//	    p.ch <- cp             // blocks — correct back-pressure
//	    return len(data), nil
//	}
//
// This is actually correct but wasteful: the select/default is a no-op because
// the blocking send below is identical to what the select does on a full
// channel. Simplified to a plain channel send.
type Pipe struct {
	ch     chan []byte
	mu     sync.Mutex
	closed bool
	err    error
	buf    []byte // residual bytes from the last partially-read chunk
}

// NewPipe creates a pipe with the given buffer capacity (number of chunks that
// can be in flight before the writer blocks).
func NewPipe(bufferSize int) *Pipe {
	if bufferSize < 1 {
		bufferSize = 8
	}
	return &Pipe{ch: make(chan []byte, bufferSize)}
}

// Write sends data into the pipe. Blocks when the buffer is full (back-
// pressure). Returns io.ErrClosedPipe if the pipe has been closed.
func (p *Pipe) Write(data []byte) (int, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	// Copy to prevent aliasing with the caller's slice.
	cp := make([]byte, len(data))
	copy(cp, data)
	p.ch <- cp // blocks on full buffer — correct back-pressure semantics
	return len(data), nil
}

// Read reads from the pipe. Blocks until data is available or the pipe is
// closed. Returns io.EOF when all data has been consumed and the pipe is done.
func (p *Pipe) Read(dst []byte) (int, error) {
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

// Close closes the write side. Subsequent writes return io.ErrClosedPipe.
// Reads drain remaining buffered data and then return io.EOF.
func (p *Pipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.ch)
	}
	return nil
}

// CloseWithError closes the pipe with an associated error.
func (p *Pipe) CloseWithError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.err = err
		close(p.ch)
	}
}

// Err returns the error passed to CloseWithError, or nil.
func (p *Pipe) Err() error {
	p.mu.Lock()
	err := p.err
	p.mu.Unlock()
	return err
}

// ─── ValuePipe ────────────────────────────────────────────────────────────────

// ValuePipe is a generic typed channel with context-aware blocking. It is used
// to stream results between the scheduler and downstream consumers without
// blocking the producer.
type ValuePipe[T any] struct {
	ch     chan T
	done   chan struct{}
	mu     sync.Mutex
	closed bool
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

// Send sends a value. Blocks if the buffer is full. Returns an error if ctx
// is cancelled or the pipe is already closed.
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

// Receive returns the read-only channel for consuming values.
func (p *ValuePipe[T]) Receive() <-chan T { return p.ch }

// Close closes the pipe. Safe to call multiple times.
func (p *ValuePipe[T]) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.ch)
		close(p.done)
	}
}
