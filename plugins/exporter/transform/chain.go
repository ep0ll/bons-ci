// Package transform provides Transformer implementations and the
// TransformerChain composite. Each transformer is independently testable and
// composable via the pipeline.
package transform

import (
	"context"
	"fmt"
	"sort"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// ─── Priority bands ────────────────────────────────────────────────────────

// Priority constants define recommended ordering bands.
// Transformers within the same band may run in any order.
const (
	PriorityEarliest    = 0   // e.g. validation
	PriorityPreProcess  = 100 // e.g. epoch clamping, config patching
	PriorityCore        = 200 // e.g. layer normalisation
	PriorityAttestation = 300 // e.g. SBOM supplementation, provenance
	PriorityAnnotation  = 400 // e.g. OCI annotation injection
	PriorityLatest      = 900 // e.g. final sanity checks
)

// ─── BaseTransformer ───────────────────────────────────────────────────────

// BaseTransformer is an embeddable struct that satisfies Name() and Priority()
// so concrete transformers only need to implement Transform().
type BaseTransformer struct {
	name     string
	priority int
}

// NewBase creates a BaseTransformer with the given name and priority.
func NewBase(name string, priority int) BaseTransformer {
	return BaseTransformer{name: name, priority: priority}
}

func (b BaseTransformer) Name() string  { return b.name }
func (b BaseTransformer) Priority() int { return b.priority }

// ─── FuncTransformer ───────────────────────────────────────────────────────

// FuncTransformer adapts a plain function into the Transformer interface.
// Useful for one-off or test transformers without boilerplate.
type FuncTransformer struct {
	BaseTransformer
	fn func(ctx context.Context, a *core.Artifact) (*core.Artifact, error)
}

// NewFuncTransformer wraps fn as a named, prioritised Transformer.
func NewFuncTransformer(
	name string,
	priority int,
	fn func(ctx context.Context, a *core.Artifact) (*core.Artifact, error),
) core.Transformer {
	return &FuncTransformer{
		BaseTransformer: NewBase(name, priority),
		fn:              fn,
	}
}

func (f *FuncTransformer) Transform(ctx context.Context, a *core.Artifact) (*core.Artifact, error) {
	return f.fn(ctx, a)
}

// ─── TransformerChain ─────────────────────────────────────────────────────

// TransformerChain is itself a Transformer that runs a sorted sequence of
// child transformers. It implements the Composite pattern: a chain can
// contain other chains, allowing hierarchical composition.
//
// TransformerChain is NOT safe for concurrent mutation; build it fully before
// passing to a Pipeline.
type TransformerChain struct {
	BaseTransformer
	children []core.Transformer
	index    map[string]struct{}
}

// NewChain creates an empty, named TransformerChain.
func NewChain(name string, priority int) *TransformerChain {
	return &TransformerChain{
		BaseTransformer: NewBase(name, priority),
		index:           make(map[string]struct{}),
	}
}

// Add appends a transformer to the chain. Duplicates are rejected.
func (c *TransformerChain) Add(t core.Transformer) error {
	if t == nil {
		return fmt.Errorf("chain %q: cannot add nil transformer", c.name)
	}
	n := t.Name()
	if _, exists := c.index[n]; exists {
		return fmt.Errorf("chain %q: transformer %q already present", c.name, n)
	}
	c.index[n] = struct{}{}
	c.children = append(c.children, t)
	sort.SliceStable(c.children, func(i, j int) bool {
		return c.children[i].Priority() < c.children[j].Priority()
	})
	return nil
}

// MustAdd calls Add and panics on error.
func (c *TransformerChain) MustAdd(t core.Transformer) *TransformerChain {
	if err := c.Add(t); err != nil {
		panic(err)
	}
	return c
}

// Transform runs all children in priority order.
func (c *TransformerChain) Transform(ctx context.Context, a *core.Artifact) (*core.Artifact, error) {
	current := a
	for _, child := range c.children {
		var err error
		current, err = child.Transform(ctx, current)
		if err != nil {
			return nil, core.NewTransformError(child.Name(), err)
		}
		if current == nil {
			return nil, fmt.Errorf("chain %q: child %q returned nil artifact", c.name, child.Name())
		}
	}
	return current, nil
}

// Children returns a snapshot of the ordered child transformer list.
func (c *TransformerChain) Children() []core.Transformer {
	cp := make([]core.Transformer, len(c.children))
	copy(cp, c.children)
	return cp
}
