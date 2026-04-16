// Package builder provides a high-level, reactive pipeline builder that
// integrates state composition, graph management, and event-driven mutation
// into a single cohesive interface.
//
// Design goals
// ────────────
//  1. Single entry point – callers import only this package for typical use.
//  2. Reactive by default – all graph mutations fire change events on the
//     shared bus; callers can subscribe to observe cascading digest changes.
//  3. Immutable snapshots – each Build() call returns an independent snapshot
//     of the current graph; previous snapshots are unaffected.
//  4. Pluggable – callers can register custom vertex factories via the embedded
//     ops.Registry.
//
// Example
//
//	b := builder.New()
//	b.Subscribe(func(e reactive.GraphEvent) {
//	    log.Printf("graph changed: %s %s", e.Kind, e.AffectedID)
//	})
//
//	alpine := b.Image("alpine:3.20")
//	compiled := alpine.Run(exec.New(
//	    exec.WithCommand("go", "build", "./..."),
//	    exec.WithWorkingDir("/src"),
//	    exec.WithMount(exec.Mount{
//	        Target: "/src",
//	        Source: b.Local("context").Output(),
//	    }),
//	))
//
//	def, err := b.Serialize(ctx, compiled.Root())
package builder

import (
	"context"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/graph"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/bons/bons-ci/client/llb/ops"
	execop "github.com/bons/bons-ci/client/llb/ops/exec"
	fileop "github.com/bons/bons-ci/client/llb/ops/file"
	"github.com/bons/bons-ci/client/llb/ops/source/image"
	"github.com/bons/bons-ci/client/llb/ops/source/local"
	"github.com/bons/bons-ci/client/llb/reactive"
	"github.com/bons/bons-ci/client/llb/state"
)

// ─── Builder ─────────────────────────────────────────────────────────────────

// Builder is the top-level orchestrator. It is safe for concurrent use.
type Builder struct {
	registry    *ops.Registry
	serializer  *marshal.Serializer
	bus         *reactive.EventBus[reactive.GraphEvent]
	constraints *core.Constraints
}

// Option is a functional option for Builder.
type Option func(*Builder)

// WithRegistry overrides the default ops registry.
func WithRegistry(r *ops.Registry) Option {
	return func(b *Builder) { b.registry = r }
}

// WithConstraints sets default build constraints (platform, caps, etc.).
func WithConstraints(c *core.Constraints) Option {
	return func(b *Builder) { b.constraints = c }
}

// New constructs a Builder with sensible defaults.
func New(opts ...Option) *Builder {
	b := &Builder{
		registry:    ops.DefaultRegistry,
		serializer:  marshal.NewSerializer(),
		bus:         reactive.NewEventBus[reactive.GraphEvent](),
		constraints: core.DefaultConstraints(),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// ─── Subscriptions ────────────────────────────────────────────────────────────

// Subscribe registers a handler that receives graph change events.
// Returns a Subscription whose Cancel() method removes the registration.
func (b *Builder) Subscribe(handler func(reactive.GraphEvent)) reactive.Subscription {
	return b.bus.Subscribe(handler)
}

// ─── Source constructors ─────────────────────────────────────────────────────

// Image returns a State backed by the given Docker/OCI image reference.
func (b *Builder) Image(ref string, opts ...image.Option) state.State {
	all := append([]image.Option{image.WithRef(ref)}, opts...)
	v, err := image.New(all...)
	if err != nil {
		// Return scratch and let the caller discover the error on Marshal.
		return state.Scratch()
	}
	b.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded})
	return state.From(v.Output())
}

// Local returns a State backed by a client-local directory.
func (b *Builder) Local(name string, opts ...local.Option) state.State {
	all := append([]local.Option{local.WithName(name)}, opts...)
	v, err := local.New(all...)
	if err != nil {
		return state.Scratch()
	}
	b.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded})
	return state.From(v.Output())
}

// Scratch returns an empty filesystem state.
func (b *Builder) Scratch() state.State { return state.Scratch() }

// ─── Operation helpers ────────────────────────────────────────────────────────

// Run executes a command vertex on top of a base state and returns an
// ExecState that exposes root and named mount outputs.
func (b *Builder) Run(base state.State, v *execop.Vertex) state.ExecState {
	b.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded})
	return base.Run(v)
}

// File applies a file op vertex to a base state and returns the result.
func (b *Builder) File(base state.State, v *fileop.Vertex) state.State {
	b.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded})
	return base.File(v)
}

// ─── Graph operations ─────────────────────────────────────────────────────────

// BuildGraph constructs a Graph from the given root state for mutation and
// traversal operations.
func (b *Builder) BuildGraph(ctx context.Context, root state.State) (*graph.Graph, error) {
	if root.IsScratch() {
		return nil, core.ErrEmptyGraph
	}
	v := root.Output().Vertex(ctx, b.constraints)
	return graph.New(ctx, v, b.constraints)
}

// Mutator returns a Mutator wrapping the given graph.
func (b *Builder) Mutator(g *graph.Graph) *graph.Mutator {
	return graph.NewMutator(g)
}

// Traversal returns a Traversal wrapping the given graph.
func (b *Builder) Traversal(g *graph.Graph) *graph.Traversal {
	return graph.NewTraversal(g)
}

// Selector returns a Selector wrapping the given graph.
func (b *Builder) Selector(g *graph.Graph) *graph.Selector {
	return graph.NewSelector(g)
}

// ─── Serialisation ────────────────────────────────────────────────────────────

// Serialize converts the graph rooted at root into a wire-format Definition.
func (b *Builder) Serialize(ctx context.Context, root state.State, opts ...core.ConstraintsOption) (*marshal.Definition, error) {
	return root.Marshal(ctx, opts...)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (b *Builder) emit(e reactive.GraphEvent) {
	b.bus.Publish(e)
}
