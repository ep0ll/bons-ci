// Package testutil provides test helpers, fakes, and builders for the
// layermerkle package. Import only from test files.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// FakeHasher — deterministic file hasher for tests
// ─────────────────────────────────────────────────────────────────────────────

// FakeHasher is a layermerkle.FileHasher that returns pre-configured hashes.
// Unknown paths return a synthetic hash derived from the path itself.
type FakeHasher struct {
	mu     sync.Mutex
	hashes map[string]layermerkle.FileHash
	calls  []string
}

// NewFakeHasher returns a FakeHasher with no pre-configured hashes.
func NewFakeHasher() *FakeHasher {
	return &FakeHasher{hashes: make(map[string]layermerkle.FileHash)}
}

// AddHash registers a hash for the given absolute path.
func (h *FakeHasher) AddHash(absPath string, hash layermerkle.FileHash) {
	h.mu.Lock()
	h.hashes[absPath] = hash
	h.mu.Unlock()
}

// Hash implements layermerkle.FileHasher.
func (h *FakeHasher) Hash(_ context.Context, absPath string) (layermerkle.FileHash, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, absPath)
	if d, ok := h.hashes[absPath]; ok {
		return d, nil
	}
	// Synthetic deterministic hash for unknown paths.
	raw := digest.FromString(absPath)
	return raw, nil
}

// Algorithm implements layermerkle.FileHasher.
func (h *FakeHasher) Algorithm() string { return "sha256" }

// Calls returns a snapshot of all paths passed to Hash.
func (h *FakeHasher) Calls() []string {
	h.mu.Lock()
	out := make([]string, len(h.calls))
	copy(out, h.calls)
	h.mu.Unlock()
	return out
}

// CallCount returns the number of Hash calls made.
func (h *FakeHasher) CallCount() int {
	h.mu.Lock()
	n := len(h.calls)
	h.mu.Unlock()
	return n
}

// Reset clears recorded calls.
func (h *FakeHasher) Reset() {
	h.mu.Lock()
	h.calls = h.calls[:0]
	h.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// FakeResolver — static layer-ownership map for tests
// ─────────────────────────────────────────────────────────────────────────────

// FakeResolver implements layermerkle.LayerFileResolver using a pre-populated
// ownership table. All DiffAbsPath values are computed as layerPath+"/"+relPath.
type FakeResolver struct {
	mu        sync.Mutex
	ownership map[string]layermerkle.LayerID   // relPath → ownerLayerID
	layerPath map[layermerkle.LayerID]string   // layerID → diffPath
	whiteouts map[string]struct{}              // relPath → deleted
}

// NewFakeResolver returns an empty FakeResolver.
func NewFakeResolver() *FakeResolver {
	return &FakeResolver{
		ownership: make(map[string]layermerkle.LayerID),
		layerPath: make(map[layermerkle.LayerID]string),
		whiteouts: make(map[string]struct{}),
	}
}

// AddFile registers that relPath is owned by layerID whose diff is at diffPath.
func (r *FakeResolver) AddFile(relPath string, layerID layermerkle.LayerID, diffPath string) {
	r.mu.Lock()
	r.ownership[relPath] = layerID
	r.layerPath[layerID] = diffPath
	r.mu.Unlock()
}

// AddWhiteout marks relPath as deleted (whiteout).
func (r *FakeResolver) AddWhiteout(relPath string) {
	r.mu.Lock()
	r.whiteouts[relPath] = struct{}{}
	r.mu.Unlock()
}

// FindOwnerLayer implements layermerkle.LayerFileResolver.
func (r *FakeResolver) FindOwnerLayer(_ context.Context, _ layermerkle.LayerStack, relPath string) (layermerkle.LayerID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.whiteouts[relPath]; ok {
		return "", layermerkle.ErrWhiteout
	}
	id, ok := r.ownership[relPath]
	if !ok {
		return "", layermerkle.ErrLayerNotFound
	}
	return id, nil
}

// DiffAbsPath implements layermerkle.LayerFileResolver.
func (r *FakeResolver) DiffAbsPath(_ context.Context, layerID layermerkle.LayerID, relPath string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path, ok := r.layerPath[layerID]
	if !ok {
		return "", layermerkle.ErrLayerNotFound
	}
	return path + "/" + relPath, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AccessEventBuilder — fluent builder for AccessEvent test fixtures
// ─────────────────────────────────────────────────────────────────────────────

// AccessEventBuilder constructs AccessEvent values for tests.
type AccessEventBuilder struct {
	ev layermerkle.AccessEvent
}

// NewAccessEvent returns a builder with sensible defaults.
func NewAccessEvent() *AccessEventBuilder {
	return &AccessEventBuilder{
		ev: layermerkle.AccessEvent{
			VertexID:   digest.FromString("default-vertex"),
			LayerStack: layermerkle.LayerStack{digest.FromString("layer-0")},
			RelPath:    "usr/bin/sh",
			AbsPath:    "/merged/usr/bin/sh",
			Mask:       0x00000001, // ACCESS
			PID:        1000,
			Timestamp:  time.Now(),
		},
	}
}

// WithVertexID sets the vertex ID from a string (hashed).
func (b *AccessEventBuilder) WithVertexID(s string) *AccessEventBuilder {
	b.ev.VertexID = digest.FromString(s)
	return b
}

// WithVertexDigest sets the vertex ID directly.
func (b *AccessEventBuilder) WithVertexDigest(d layermerkle.VertexID) *AccessEventBuilder {
	b.ev.VertexID = d
	return b
}

// WithLayerStack sets the layer stack from string identifiers.
func (b *AccessEventBuilder) WithLayerStack(layers ...string) *AccessEventBuilder {
	b.ev.LayerStack = make(layermerkle.LayerStack, len(layers))
	for i, l := range layers {
		b.ev.LayerStack[i] = digest.FromString(l)
	}
	return b
}

// WithRelPath sets the relative file path.
func (b *AccessEventBuilder) WithRelPath(p string) *AccessEventBuilder {
	b.ev.RelPath = p
	return b
}

// WithAbsPath sets the absolute merged-view path.
func (b *AccessEventBuilder) WithAbsPath(p string) *AccessEventBuilder {
	b.ev.AbsPath = p
	return b
}

// WithMask sets the fanotify mask.
func (b *AccessEventBuilder) WithMask(m uint64) *AccessEventBuilder {
	b.ev.Mask = m
	return b
}

// WithPID sets the triggering PID.
func (b *AccessEventBuilder) WithPID(pid int32) *AccessEventBuilder {
	b.ev.PID = pid
	return b
}

// Build returns the constructed AccessEvent.
func (b *AccessEventBuilder) Build() *layermerkle.AccessEvent {
	cp := b.ev
	return &cp
}

// ─────────────────────────────────────────────────────────────────────────────
// LayerFixture — pre-wired layer for engine tests
// ─────────────────────────────────────────────────────────────────────────────

// LayerFixture is a registered layer with a known ID and diff path.
type LayerFixture struct {
	ID       layermerkle.LayerID
	DiffPath string
	Info     *layermerkle.LayerInfo
}

// NewLayerFixture creates a LayerFixture identified by name.
func NewLayerFixture(name, diffPath string) *LayerFixture {
	id := digest.FromString(name)
	info := &layermerkle.LayerInfo{
		ID:       id,
		DiffPath: diffPath,
		Labels:   map[string]string{"fixture.name": name},
	}
	return &LayerFixture{ID: id, DiffPath: diffPath, Info: info}
}

// Register adds this layer to the given registry.
func (f *LayerFixture) Register(r *layermerkle.LayerRegistry) error {
	return r.Register(f.Info)
}

// ─────────────────────────────────────────────────────────────────────────────
// TreeCollector — records finalized MerkleTrees
// ─────────────────────────────────────────────────────────────────────────────

// TreeCollector accumulates MerkleTrees for assertion in tests.
type TreeCollector struct {
	mu    sync.Mutex
	trees []*layermerkle.MerkleTree
}

// Collect returns a func(t) suitable for use as the Engine's OnTree callback.
func (c *TreeCollector) Collect() func(*layermerkle.MerkleTree) {
	return func(t *layermerkle.MerkleTree) {
		c.mu.Lock()
		c.trees = append(c.trees, t)
		c.mu.Unlock()
	}
}

// Trees returns all collected trees.
func (c *TreeCollector) Trees() []*layermerkle.MerkleTree {
	c.mu.Lock()
	out := make([]*layermerkle.MerkleTree, len(c.trees))
	copy(out, c.trees)
	c.mu.Unlock()
	return out
}

// WaitFor blocks until at least n trees are collected or ctx expires.
func (c *TreeCollector) WaitFor(ctx context.Context, n int) bool {
	deadline := time.NewTicker(2 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			if c.Len() >= n {
				return true
			}
		}
	}
}

// Len returns the number of collected trees.
func (c *TreeCollector) Len() int {
	c.mu.Lock()
	n := len(c.trees)
	c.mu.Unlock()
	return n
}

// MakeLayerStack returns a LayerStack from a list of name strings.
func MakeLayerStack(names ...string) layermerkle.LayerStack {
	stack := make(layermerkle.LayerStack, len(names))
	for i, n := range names {
		stack[i] = digest.FromString(n)
	}
	return stack
}

// MakeVertexID returns a VertexID from a name string.
func MakeVertexID(name string) layermerkle.VertexID {
	return digest.FromString(name)
}

// MakeFileHash returns a deterministic FileHash from a content string.
func MakeFileHash(content string) layermerkle.FileHash {
	return digest.FromString(content)
}

// EventBatch returns n AccessEvents all sharing the same vertex and layer stack.
func EventBatch(vertexName string, layers []string, paths []string) []*layermerkle.AccessEvent {
	events := make([]*layermerkle.AccessEvent, 0, len(paths))
	for _, p := range paths {
		ev := NewAccessEvent().
			WithVertexID(vertexName).
			WithLayerStack(layers...).
			WithRelPath(p).
			WithAbsPath(fmt.Sprintf("/merged/%s", p)).
			Build()
		events = append(events, ev)
	}
	return events
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayFixture — real on-disk layer tree for integration tests
// ─────────────────────────────────────────────────────────────────────────────

// OverlayFixture creates a temporary directory tree that mirrors the structure
// of a containerd/Docker overlay filesystem. Use in integration tests that
// need real files on disk (e.g. for SHA-256 hashing, whiteout detection).
type OverlayFixture struct {
	// Root is the temp directory containing all overlay subdirectories.
	Root string
	// MergedDir is the simulated merged view path (not actually mounted).
	MergedDir string
	// UpperDir is the writable layer.
	UpperDir string
	// WorkDir is the overlay work directory.
	WorkDir string
	// LowerDirs are the read-only layers, topmost first.
	LowerDirs []string
}

// NewOverlayFixture creates a temporary overlay structure with n lower layers.
// Call Cleanup() or register t.Cleanup(fix.Cleanup) when the test finishes.
func NewOverlayFixture(root string, lowerLayerCount int) (*OverlayFixture, error) {
	if lowerLayerCount < 1 {
		lowerLayerCount = 1
	}
	merged := filepath.Join(root, "merged")
	upper := filepath.Join(root, "upper")
	work := filepath.Join(root, "work")
	for _, d := range []string{merged, upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("testutil: mkdir %s: %w", d, err)
		}
	}
	lowerDirs := make([]string, lowerLayerCount)
	for i := range lowerLayerCount {
		d := filepath.Join(root, fmt.Sprintf("lower%d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("testutil: mkdir lower%d: %w", i, err)
		}
		lowerDirs[i] = d
	}
	return &OverlayFixture{
		Root:      root,
		MergedDir: merged,
		UpperDir:  upper,
		WorkDir:   work,
		LowerDirs: lowerDirs,
	}, nil
}

// WriteLayerFile writes content to relPath inside the Nth lower layer.
func (f *OverlayFixture) WriteLayerFile(layerIndex int, relPath, content string) error {
	if layerIndex < 0 || layerIndex >= len(f.LowerDirs) {
		return fmt.Errorf("testutil: layer index %d out of range", layerIndex)
	}
	abs := filepath.Join(f.LowerDirs[layerIndex], filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// WriteUpperFile writes content to relPath inside the upper (writable) layer.
func (f *OverlayFixture) WriteUpperFile(relPath, content string) error {
	abs := filepath.Join(f.UpperDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// Cleanup removes all temporary directories.
func (f *OverlayFixture) Cleanup() { os.RemoveAll(f.Root) }
