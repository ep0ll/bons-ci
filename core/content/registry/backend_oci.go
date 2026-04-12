package registry

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// ociBackend implements RegistryBackend using containerd's OCIRegistry.
// Connection pooling is delegated to ociConnPool (one TLS session per host).
type ociBackend struct {
	pool *ociConnPool
}

// NewOCIBackend creates a RegistryBackend backed by containerd's OCIRegistry.
// The provided opts are forwarded to registry.NewOCIRegistry.
func NewOCIBackend(opts ...registry.Opt) RegistryBackend {
	return &ociBackend{pool: newOCIConnPool(opts)}
}

func (b *ociBackend) Resolve(ctx context.Context, ref string) (string, v1.Descriptor, error) {
	reg, err := b.pool.get(ctx, ref)
	if err != nil {
		return "", v1.Descriptor{}, fmt.Errorf("backend: connect %q: %w", ref, err)
	}
	name, desc, err := reg.Resolve(ctx)
	if err != nil {
		return "", v1.Descriptor{}, fmt.Errorf("backend: resolve %q: %w", ref, err)
	}
	return name, desc, nil
}

func (b *ociBackend) Fetch(ctx context.Context, ref string, desc v1.Descriptor) (io.ReadCloser, error) {
	reg, err := b.pool.get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("backend: connect %q: %w", ref, err)
	}
	fetcher, err := reg.Fetcher(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("backend: fetcher %q: %w", ref, err)
	}
	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("backend: fetch %s from %q: %w", desc.Digest, ref, err)
	}
	return rc, nil
}

func (b *ociBackend) Push(ctx context.Context, ref string, desc v1.Descriptor) (content.Writer, error) {
	reg, err := b.pool.get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("backend: connect %q: %w", ref, err)
	}
	pusher, err := reg.Pusher(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("backend: pusher %q: %w", ref, err)
	}
	w, err := pusher.Push(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("backend: push %s to %q: %w", desc.Digest, ref, err)
	}
	return w, nil
}

// Close stops the idle-eviction goroutine.
func (b *ociBackend) Close() { b.pool.close() }

// compile-time check
var _ RegistryBackend = (*ociBackend)(nil)

// ---------------------------------------------------------------------------
// Host-keyed OCI connection pool
// ---------------------------------------------------------------------------

const connIdleTimeout = 5 * time.Minute

// connEntry is padded so the hot lastUsed field lives in its own cache line,
// preventing false-sharing with adjacent entries.
type connEntry struct {
	lastUsed int64 // unix nano; accessed with atomic ops (hot field)
	_        [56]byte
	reg      *registry.OCIRegistry // cold: read-only after construction
	mu       sync.Mutex            // guards lazy init of reg
}

// ociConnPool maintains one OCIRegistry per registry host using sync.Map for
// lock-free loads on the read-dominant hot path.
type ociConnPool struct {
	opts    []registry.Opt
	m       sync.Map     // host string → *connEntry
	once    sync.Once
	closeCh chan struct{}
}

func newOCIConnPool(opts []registry.Opt) *ociConnPool {
	return &ociConnPool{opts: opts, closeCh: make(chan struct{})}
}

// get returns the pooled OCIRegistry for the registry host in ref.
// It lazy-starts the idle-eviction goroutine on first call.
func (p *ociConnPool) get(ctx context.Context, ref string) (*registry.OCIRegistry, error) {
	p.once.Do(p.startEviction)

	host := ociHost(ref)
	now := time.Now().UnixNano()

	// Hot path: lock-free load.
	if v, ok := p.m.Load(host); ok {
		e := v.(*connEntry)
		atomic.StoreInt64(&e.lastUsed, now)
		return e.reg, nil
	}

	// Miss: store a placeholder under per-entry lock to avoid duplicate dials.
	entry := &connEntry{}
	actual, loaded := p.m.LoadOrStore(host, entry)
	if loaded {
		entry = actual.(*connEntry)
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Double-check after acquiring lock.
	if entry.reg != nil {
		atomic.StoreInt64(&entry.lastUsed, now)
		return entry.reg, nil
	}

	reg, err := registry.NewOCIRegistry(ctx, ref, p.opts...)
	if err != nil {
		p.m.Delete(host) // don't cache failed connections
		return nil, fmt.Errorf("backend: new OCI registry %q: %w", ref, err)
	}
	entry.reg = reg
	atomic.StoreInt64(&entry.lastUsed, now)
	return reg, nil
}

func (p *ociConnPool) close() {
	select {
	case <-p.closeCh:
	default:
		close(p.closeCh)
	}
}

func (p *ociConnPool) startEviction() {
	ticker := time.NewTicker(connIdleTimeout / 2)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-p.closeCh:
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-connIdleTimeout).UnixNano()
				p.m.Range(func(k, v any) bool {
					if atomic.LoadInt64(&v.(*connEntry).lastUsed) < cutoff {
						p.m.Delete(k)
					}
					return true
				})
			}
		}
	}()
}

// ociHost extracts the registry hostname from an OCI reference string.
// "docker.io/library/nginx:latest" → "docker.io"
func ociHost(ref string) string {
	for i, c := range ref {
		if c == '/' || c == ':' || c == '@' {
			return ref[:i]
		}
	}
	return ref
}
