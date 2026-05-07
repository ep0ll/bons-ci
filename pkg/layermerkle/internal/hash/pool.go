package hash

import (
	"crypto/sha256"
	"hash"
	"io"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Pool — reusable hash.Hash instances to eliminate per-file allocations
// ─────────────────────────────────────────────────────────────────────────────

// Pool holds pre-allocated hash.Hash instances of a single algorithm.
// Callers must call Release after use; the hash is Reset() before re-pooling.
type Pool struct {
	pool sync.Pool
}

// SHA256Pool is the package-level singleton SHA-256 pool.
var SHA256Pool = &Pool{
	pool: sync.Pool{New: func() any { return sha256.New() }},
}

// Acquire returns a reset hash.Hash from the pool.
func (p *Pool) Acquire() hash.Hash {
	h := p.pool.Get().(hash.Hash)
	h.Reset()
	return h
}

// Release returns h to the pool after resetting it.
func (p *Pool) Release(h hash.Hash) {
	h.Reset()
	p.pool.Put(h)
}

// SumFile hashes the content of r using the given pool and returns the digest bytes.
// Uses a pooled copy buffer to minimise allocations on the hot path.
func SumFile(r io.Reader, p *Pool) ([]byte, int64, error) {
	h := p.Acquire()
	defer p.Release(h)
	buf := copyBufPool.Get().([]byte)
	defer copyBufPool.Put(buf)
	n, err := io.CopyBuffer(h, r, buf)
	if err != nil {
		return nil, n, err
	}
	return h.Sum(nil), n, nil
}

// SumBytes hashes the given bytes using the pool and returns the digest.
// Inlines the call to avoid heap allocation for small inputs.
func SumBytes(b []byte, p *Pool) []byte {
	h := p.Acquire()
	defer p.Release(h)
	h.Write(b)
	return h.Sum(nil)
}

// 32 KiB copy buffer matches common filesystem block sizes.
const copyBufSize = 32 << 10

var copyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, copyBufSize)
		return buf
	},
}
