package chunk

import "sync"

// Pool is a tiered buffer pool that prevents GC pressure from streaming
// hash computations. It maintains separate pools for common buffer sizes
// to avoid fragmentation.
type Pool struct {
	small  sync.Pool // 4KB buffers
	medium sync.Pool // 32KB buffers
	large  sync.Pool // 64KB buffers
}

const (
	sizeSmall  = 4 * 1024
	sizeMedium = 32 * 1024
	sizeLarge  = 64 * 1024
)

// NewPool creates a new tiered buffer pool.
func NewPool() *Pool {
	return &Pool{
		small: sync.Pool{
			New: func() any { return make([]byte, sizeSmall) },
		},
		medium: sync.Pool{
			New: func() any { return make([]byte, sizeMedium) },
		},
		large: sync.Pool{
			New: func() any { return make([]byte, sizeLarge) },
		},
	}
}

// Get returns a byte slice of at least the requested size.
// The returned slice may be larger than requested; the caller should
// use only buf[:size] for their needs. Callers must return the buffer
// via [Pool.Put] when done.
func (p *Pool) Get(size int) []byte {
	switch {
	case size <= sizeSmall:
		return p.small.Get().([]byte)
	case size <= sizeMedium:
		return p.medium.Get().([]byte)
	case size <= sizeLarge:
		return p.large.Get().([]byte)
	default:
		// Oversized: allocate directly, don't pool
		return make([]byte, size)
	}
}

// Put returns a buffer to the pool. Buffers not matching a pool tier
// are silently discarded to avoid pool pollution.
func (p *Pool) Put(buf []byte) {
	switch cap(buf) {
	case sizeSmall:
		p.small.Put(buf[:sizeSmall])
	case sizeMedium:
		p.medium.Put(buf[:sizeMedium])
	case sizeLarge:
		p.large.Put(buf[:sizeLarge])
	default:
		// Non-standard size: let GC handle it
	}
}
