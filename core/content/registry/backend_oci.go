package registry

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// ---------------------------------------------------------------------------
// OCI Adapter — implements RegistryBackend via containerd transfer/registry
// ---------------------------------------------------------------------------

// ociBackend implements RegistryBackend using containerd's OCIRegistry.
// It maintains a connection pool of OCIRegistry instances keyed by reference,
// avoiding redundant TLS handshakes and token exchanges for repeated
// operations against the same registry host.
type ociBackend struct {
	opts []registry.Opt

	mu    sync.RWMutex
	cache map[string]*registry.OCIRegistry
}

// NewOCIBackend creates a RegistryBackend backed by containerd's OCIRegistry.
// The provided opts are applied when constructing new registry connections.
func NewOCIBackend(opts ...registry.Opt) RegistryBackend {
	return &ociBackend{
		opts:  opts,
		cache: make(map[string]*registry.OCIRegistry),
	}
}

// Resolve implements RegistryBackend.
func (b *ociBackend) Resolve(ctx context.Context, ref string) (string, v1.Descriptor, error) {
	reg, err := b.getOrCreate(ctx, ref)
	if err != nil {
		return "", v1.Descriptor{}, err
	}

	name, desc, err := reg.Resolve(ctx)
	if err != nil {
		return "", v1.Descriptor{}, fmt.Errorf("registry: resolve %q: %w", ref, err)
	}
	return name, desc, nil
}

// Fetch implements RegistryBackend.
func (b *ociBackend) Fetch(ctx context.Context, ref string, desc v1.Descriptor) (io.ReadCloser, error) {
	reg, err := b.getOrCreate(ctx, ref)
	if err != nil {
		return nil, err
	}

	fetcher, err := reg.Fetcher(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("registry: create fetcher for %q: %w", ref, err)
	}

	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("registry: fetch %s from %q: %w", desc.Digest, ref, err)
	}
	return rc, nil
}

// Push implements RegistryBackend.
func (b *ociBackend) Push(ctx context.Context, ref string, desc v1.Descriptor) (content.Writer, error) {
	reg, err := b.getOrCreate(ctx, ref)
	if err != nil {
		return nil, err
	}

	pusher, err := reg.Pusher(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("registry: create pusher for %q: %w", ref, err)
	}

	w, err := pusher.Push(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("registry: push %s to %q: %w", desc.Digest, ref, err)
	}
	return w, nil
}

// getOrCreate returns a cached OCIRegistry or creates a new one.
// Uses double-check locking: fast read-lock path, then write-lock on miss.
func (b *ociBackend) getOrCreate(ctx context.Context, ref string) (*registry.OCIRegistry, error) {
	b.mu.RLock()
	reg, ok := b.cache[ref]
	b.mu.RUnlock()
	if ok {
		return reg, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check after acquiring write lock.
	if reg, ok = b.cache[ref]; ok {
		return reg, nil
	}

	reg, err := registry.NewOCIRegistry(ctx, ref, b.opts...)
	if err != nil {
		return nil, fmt.Errorf("registry: create connection for %q: %w", ref, err)
	}

	b.cache[ref] = reg
	return reg, nil
}

// compile-time check
var _ RegistryBackend = (*ociBackend)(nil)
