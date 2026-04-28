package layer

import (
	"sync"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// Chain represents an ordered sequence of stacked filesystem layers,
// from bottom (index 0) to top (index len-1). Immutable after construction.
type Chain struct {
	layers []core.LayerID
	index  map[string]int
}

// NewChain creates a Chain from an ordered slice of layer IDs.
func NewChain(layers []core.LayerID) *Chain {
	c := &Chain{
		layers: make([]core.LayerID, len(layers)),
		index:  make(map[string]int, len(layers)),
	}
	copy(c.layers, layers)
	for i, l := range c.layers {
		c.index[l.String()] = i
	}
	return c
}

// Layers returns a copy of the ordered layer slice.
func (c *Chain) Layers() []core.LayerID {
	out := make([]core.LayerID, len(c.layers))
	copy(out, c.layers)
	return out
}

// Depth returns the number of layers.
func (c *Chain) Depth() int { return len(c.layers) }

// Contains reports whether the chain includes the given layer.
func (c *Chain) Contains(id core.LayerID) bool {
	_, ok := c.index[id.String()]
	return ok
}

// Position returns the zero-based position, or -1 if not found.
func (c *Chain) Position(id core.LayerID) int {
	pos, ok := c.index[id.String()]
	if !ok {
		return -1
	}
	return pos
}

// Top returns the uppermost layer.
func (c *Chain) Top() core.LayerID {
	if len(c.layers) == 0 {
		return core.LayerID{}
	}
	return c.layers[len(c.layers)-1]
}

// Bottom returns the base layer.
func (c *Chain) Bottom() core.LayerID {
	if len(c.layers) == 0 {
		return core.LayerID{}
	}
	return c.layers[0]
}

// Above returns all layers strictly above the given layer.
func (c *Chain) Above(id core.LayerID) []core.LayerID {
	pos := c.Position(id)
	if pos < 0 || pos >= len(c.layers)-1 {
		return nil
	}
	out := make([]core.LayerID, len(c.layers)-pos-1)
	copy(out, c.layers[pos+1:])
	return out
}

// Freeze returns a deep copy of the chain.
func (c *Chain) Freeze() *Chain { return NewChain(c.layers) }

// ChainBuilder constructs a Chain incrementally. Thread-safe.
type ChainBuilder struct {
	mu     sync.Mutex
	layers []core.LayerID
}

// NewChainBuilder creates a new builder.
func NewChainBuilder() *ChainBuilder { return &ChainBuilder{} }

// Push appends a layer to the top.
func (b *ChainBuilder) Push(id core.LayerID) {
	b.mu.Lock()
	b.layers = append(b.layers, id)
	b.mu.Unlock()
}

// Build finalizes and returns an immutable Chain.
func (b *ChainBuilder) Build() *Chain {
	b.mu.Lock()
	defer b.mu.Unlock()
	return NewChain(b.layers)
}

// Depth returns the current layer count.
func (b *ChainBuilder) Depth() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.layers)
}
