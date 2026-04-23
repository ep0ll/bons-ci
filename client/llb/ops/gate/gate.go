// Package gate provides a policy validation vertex that inspects a build graph
// node and either passes it through unchanged or aborts the build with a
// descriptive error.
//
// The gate is evaluated at definition (marshal) time, not at build execution
// time. This makes it suitable for compile-time policy enforcement such as:
//   - Requiring all image sources to have a pinned digest.
//   - Blocking use of specific base images.
//   - Enforcing that secrets are not exposed as environment variables.
//   - Checking that all local sources declare include patterns.
//
// Example – require pinned image digests
//
//	pinnedOnly := gate.PolicyFunc(func(ctx context.Context, v core.Vertex, c *core.Constraints) error {
//	    imgV, ok := v.(*image.Vertex)
//	    if !ok {
//	        return nil
//	    }
//	    if imgV.Config().Checksum == "" {
//	        return fmt.Errorf("image %q must have a pinned digest", imgV.NormalisedRef())
//	    }
//	    return nil
//	})
//
//	gate, _ := gate.New(gate.WithSubject(imageVertex.Output()), gate.WithPolicy(pinnedOnly))
package gate

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Policy ──────────────────────────────────────────────────────────────────

// Policy is an inspectable rule that can approve or reject a vertex.
// Returning nil means the vertex passes; returning an error rejects it.
type Policy interface {
	// Evaluate inspects the subject vertex under the given constraints.
	// Returning a non-nil error causes the gate to abort with that error.
	Evaluate(ctx context.Context, subject core.Vertex, c *core.Constraints) error
	// Name returns a short identifier for this policy (used in error messages).
	Name() string
}

// PolicyFunc adapts a plain function to the Policy interface.
type PolicyFunc struct {
	name string
	fn   func(ctx context.Context, v core.Vertex, c *core.Constraints) error
}

// NewPolicyFunc creates a named Policy from a function.
func NewPolicyFunc(name string, fn func(ctx context.Context, v core.Vertex, c *core.Constraints) error) *PolicyFunc {
	return &PolicyFunc{name: name, fn: fn}
}

func (p *PolicyFunc) Evaluate(ctx context.Context, v core.Vertex, c *core.Constraints) error {
	return p.fn(ctx, v, c)
}
func (p *PolicyFunc) Name() string { return p.name }

// ─── Built-in policies ───────────────────────────────────────────────────────

// AllOf combines multiple policies: all must pass.
func AllOf(policies ...Policy) Policy {
	return NewPolicyFunc("all-of", func(ctx context.Context, v core.Vertex, c *core.Constraints) error {
		for _, p := range policies {
			if err := p.Evaluate(ctx, v, c); err != nil {
				return fmt.Errorf("[%s] %w", p.Name(), err)
			}
		}
		return nil
	})
}

// AnyOf combines multiple policies: at least one must pass.
func AnyOf(policies ...Policy) Policy {
	return NewPolicyFunc("any-of", func(ctx context.Context, v core.Vertex, c *core.Constraints) error {
		var lastErr error
		for _, p := range policies {
			if err := p.Evaluate(ctx, v, c); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		return lastErr
	})
}

// VertexTypeAllowed creates a policy that rejects vertices not in the allowed set.
func VertexTypeAllowed(allowed ...core.VertexType) Policy {
	set := make(map[core.VertexType]struct{}, len(allowed))
	for _, vt := range allowed {
		set[vt] = struct{}{}
	}
	return NewPolicyFunc("vertex-type-allowed", func(_ context.Context, v core.Vertex, _ *core.Constraints) error {
		if _, ok := set[v.Type()]; !ok {
			return fmt.Errorf("vertex type %q is not in the allowed set %v", v.Type(), allowed)
		}
		return nil
	})
}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds all parameters for the gate vertex.
type Config struct {
	// Subject is the vertex being inspected. Required.
	Subject core.Output
	// Policy is evaluated against Subject at marshal time. Required.
	Policy Policy
	// FailOnReject causes the gate to return a hard error rather than
	// propagating scratch on rejection. Default: true.
	FailOnReject bool
	// FallbackOnReject is the output used when FailOnReject is false and the
	// policy rejects. Nil = scratch.
	FallbackOnReject core.Output
	Constraints      core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithSubject(out core.Output) Option  { return func(c *Config) { c.Subject = out } }
func WithPolicy(p Policy) Option          { return func(c *Config) { c.Policy = p } }
func WithFailOnReject(v bool) Option      { return func(c *Config) { c.FailOnReject = v } }
func WithFallback(out core.Output) Option { return func(c *Config) { c.FallbackOnReject = out } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex is the gate op. At marshal time it evaluates the policy against the
// subject. If the policy passes, the gate is transparent and the subject's
// MarshaledVertex is returned directly. If the policy rejects:
//   - When FailOnReject is true (default), a PolicyRejectedError is returned.
//   - When FailOnReject is false, FallbackOnReject is used instead.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a gate vertex. FailOnReject defaults to true.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{FailOnReject: true}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Subject == nil {
		return nil, fmt.Errorf("gate.New: Subject is required")
	}
	if cfg.Policy == nil {
		return nil, fmt.Errorf("gate.New: Policy is required")
	}
	return &Vertex{config: cfg}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeGate }

func (v *Vertex) Inputs() []core.Edge {
	var edges []core.Edge
	if v.config.Subject != nil {
		edges = append(edges, core.Edge{
			Vertex: v.config.Subject.Vertex(context.Background(), nil), Index: 0,
		})
	}
	if v.config.FallbackOnReject != nil {
		edges = append(edges, core.Edge{
			Vertex: v.config.FallbackOnReject.Vertex(context.Background(), nil), Index: 0,
		})
	}
	return edges
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "gate-validated subject"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Subject == nil {
		return &core.ValidationError{Field: "Subject", Cause: fmt.Errorf("must not be nil")}
	}
	if v.config.Policy == nil {
		return &core.ValidationError{Field: "Policy", Cause: fmt.Errorf("must not be nil")}
	}
	return nil
}

// Marshal evaluates the policy. On pass it returns the subject's MarshaledVertex.
// On rejection it either errors or delegates to FallbackOnReject.
func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	subjectVtx := v.config.Subject.Vertex(ctx, c)
	if subjectVtx == nil {
		return nil, fmt.Errorf("gate.Marshal: subject vertex is nil")
	}

	// Run the policy.
	policyErr := v.config.Policy.Evaluate(ctx, subjectVtx, c)
	if policyErr != nil {
		if v.config.FailOnReject {
			return nil, &core.PolicyRejectedError{
				Policy: v.config.Policy.Name(),
				Reason: policyErr.Error(),
			}
		}
		// Use fallback instead.
		if v.config.FallbackOnReject != nil {
			fallbackVtx := v.config.FallbackOnReject.Vertex(ctx, c)
			if fallbackVtx != nil {
				mv, err := fallbackVtx.Marshal(ctx, c)
				if err != nil {
					return nil, fmt.Errorf("gate.Marshal fallback: %w", err)
				}
				dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
				return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
			}
		}
		// Fallback is scratch.
		return v.marshalScratch(c, h)
	}

	// Policy passed – return the subject transparently.
	mv, err := subjectVtx.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("gate.Marshal subject: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) marshalScratch(c *core.Constraints, h *marshal.CacheHandle) (*core.MarshaledVertex, error) {
	pop, md := marshal.MarshalConstraints(c, &v.config.Constraints)
	pop.Platform = nil
	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{Identifier: "scratch://"}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, err
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	newCfg := v.config
	switch len(inputs) {
	case 0:
	case 1:
		newCfg.Subject = &edgeOutput{edge: inputs[0]}
	case 2:
		newCfg.Subject = &edgeOutput{edge: inputs[0]}
		newCfg.FallbackOnReject = &edgeOutput{edge: inputs[1]}
	default:
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(), Got: len(inputs), Want: "1 or 2",
		}
	}
	return &Vertex{config: newCfg}, nil
}

// EvaluateNow runs the policy outside of Marshal, returning the policy error
// (if any) without modifying the graph. Useful for pre-flight checks.
func (v *Vertex) EvaluateNow(ctx context.Context, c *core.Constraints) error {
	subjectVtx := v.config.Subject.Vertex(ctx, c)
	if subjectVtx == nil {
		return nil
	}
	return v.config.Policy.Evaluate(ctx, subjectVtx, c)
}

func (v *Vertex) Output() core.Output { return &gateOutput{v: v} }

type gateOutput struct{ v *Vertex }

func (o *gateOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *gateOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

type edgeOutput struct{ edge core.Edge }

func (e *edgeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return e.edge.Vertex }
func (e *edgeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := e.edge.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.edge.Index)}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
