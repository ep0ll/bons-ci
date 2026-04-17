// Package builder provides the top-level reactive orchestrator that integrates
// state composition, DAG management, and event-driven mutation.
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

// WithConstraints sets default build constraints.
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

// ─── Subscriptions ───────────────────────────────────────────────────────────

// Subscribe registers a handler for graph change events.
func (b *Builder) Subscribe(handler func(reactive.GraphEvent)) reactive.Subscription {
	return b.bus.Subscribe(handler)
}

// ─── Source constructors ─────────────────────────────────────────────────────

// Image returns a State backed by the given Docker/OCI image reference.
func (b *Builder) Image(ref string, opts ...image.Option) state.State {
	all := append([]image.Option{image.WithRef(ref)}, opts...)
	v, err := image.New(all...)
	if err != nil {
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

// ─── Composition helpers ──────────────────────────────────────────────────────

// File applies a file op to a base state.
func (b *Builder) File(base state.State, v *fileop.Vertex) state.State {
	b.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded})
	return base.File(v)
}

// Run executes a command on a base state.
func (b *Builder) Run(base state.State, v *execop.Vertex) state.ExecState {
	b.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded})
	return base.Run(v)
}

// ─── DAG operations ──────────────────────────────────────────────────────────

// BuildDAG constructs a DAG from the given root state.
func (b *Builder) BuildDAG(ctx context.Context, root state.State) (*graph.DAG, error) {
	if root.IsScratch() {
		return nil, core.ErrEmptyGraph
	}
	v := root.Output().Vertex(ctx, b.constraints)
	return graph.New(ctx, v, b.constraints)
}

// MergeDAGs merges multiple DAGs into one, deduplicating by content address.
func (b *Builder) MergeDAGs(ctx context.Context, dags ...*graph.DAG) (*graph.DAG, error) {
	return graph.MergeDAGs(ctx, b.constraints, dags...)
}

// Mutator returns a Mutator wrapping the given DAG.
func (b *Builder) Mutator(d *graph.DAG) *graph.Mutator { return graph.NewMutator(d) }

// Traversal returns a Traversal wrapping the given DAG.
func (b *Builder) Traversal(d *graph.DAG) *graph.Traversal { return graph.NewTraversal(d) }

// Selector returns a Selector wrapping the given DAG.
func (b *Builder) Selector(d *graph.DAG) *graph.Selector { return graph.NewSelector(d) }

// SubDAG extracts the sub-graph reachable from the given roots.
func (b *Builder) SubDAG(
	ctx context.Context,
	d *graph.DAG,
	roots []core.VertexID,
	include func(core.VertexID, core.Vertex) bool,
) *graph.DAG {
	return graph.SubDAG(ctx, d, b.constraints, roots, include)
}

// DiffDAGs computes the structural difference between two DAG snapshots.
func (b *Builder) DiffDAGs(ctx context.Context, before, after *graph.DAG) graph.DiffResult {
	return graph.DiffDAGs(ctx, before, after)
}

// ─── Serialisation ───────────────────────────────────────────────────────────

// Serialize converts the graph rooted at root into a wire-format Definition.
func (b *Builder) Serialize(
	ctx context.Context,
	root state.State,
	opts ...core.ConstraintsOption,
) (*marshal.Definition, error) {
	return root.Marshal(ctx, opts...)
}

// ─── Constraints accessors ───────────────────────────────────────────────────

// Constraints returns the builder's default constraints.
func (b *Builder) Constraints() *core.Constraints { return b.constraints }

// WithBuildArg returns a new Builder with a build argument added to constraints.
func (b *Builder) WithBuildArg(key, value string) *Builder {
	nb := *b
	newC := b.constraints.Clone()
	newC.BuildArgs[key] = value
	nb.constraints = newC
	return &nb
}

// ─── internal ────────────────────────────────────────────────────────────────

func (b *Builder) emit(e reactive.GraphEvent) { b.bus.Publish(e) }
