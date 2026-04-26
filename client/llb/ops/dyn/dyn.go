// Package dyn provides the DynOp vertex – an extension of the exec op that
// produces an OPA/Rego policy document as its primary artefact. The policy
// output can then be fed into a gate.Vertex for runtime validation, or used
// with conditional.Vertex / matrix expansion for data-driven graph
// construction.
//
// At marshal time, DynOp delegates to an inner exec vertex. The policy
// evaluation itself happens post-solve via the PolicyReader interface, keeping
// the LLB layer purely declarative.
//
// Example
//
//	alpine, _ := image.New(image.WithRef("alpine:3.20"))
//	dv, _ := dyn.New(
//	    dyn.WithRootMount(alpine.Output()),
//	    dyn.WithCommand("sh", "-c", "cat /policies/check.rego > /output/policy.rego"),
//	    dyn.WithPolicyPath("/output/policy.rego"),
//	    dyn.WithPolicyFormat(dyn.PolicyFormatRego),
//	)
package dyn

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	execop "github.com/bons/bons-ci/client/llb/ops/exec"
	"github.com/moby/buildkit/solver/pb"
)

// ─── PolicyFormat ────────────────────────────────────────────────────────────

// PolicyFormat identifies the policy language.
type PolicyFormat string

const (
	PolicyFormatRego PolicyFormat = "rego"
	PolicyFormatJSON PolicyFormat = "json"
)

// ─── PolicyReader ────────────────────────────────────────────────────────────

// PolicyReader reads policy content from a solved dyn op's output filesystem.
// This is a port interface – the solver subsystem provides the concrete
// implementation after the exec completes.
type PolicyReader interface {
	// ReadPolicy reads the policy file at the configured path from the
	// result filesystem. Returns the raw policy content.
	ReadPolicy(ctx context.Context) ([]byte, error)
}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds all parameters for the dyn op.
type Config struct {
	// ExecOpts are the options forwarded to the inner exec vertex.
	ExecOpts []execop.Option
	// PolicyPath is where the exec writes the policy file. Required.
	PolicyPath string
	// PolicyFormat identifies the policy language. Defaults to Rego.
	PolicyFormat PolicyFormat
	// Description is a human-readable label.
	Description string
	Constraints core.Constraints
	// Observer receives lifecycle notifications (optional).
	Observer Observer
	// Hooks are invoked at marshal boundaries (optional).
	Hooks []Hook
	// TimeoutSecs is the maximum evaluation time in seconds (0 = unlimited).
	TimeoutSecs int
}

// Option is a functional option for Config.
type Option func(*Config)

func WithCommand(args ...string) Option {
	return func(c *Config) { c.ExecOpts = append(c.ExecOpts, execop.WithCommand(args...)) }
}
func WithRootMount(source core.Output) Option {
	return func(c *Config) { c.ExecOpts = append(c.ExecOpts, execop.WithRootMount(source, false)) }
}
func WithMount(m execop.Mount) Option {
	return func(c *Config) { c.ExecOpts = append(c.ExecOpts, execop.WithMount(m)) }
}
func WithWorkingDir(dir string) Option {
	return func(c *Config) { c.ExecOpts = append(c.ExecOpts, execop.WithWorkingDir(dir)) }
}
func WithEnv(key, value string) Option {
	return func(c *Config) { c.ExecOpts = append(c.ExecOpts, execop.WithEnv(key, value)) }
}
func WithPolicyPath(path string) Option {
	return func(c *Config) { c.PolicyPath = path }
}
func WithPolicyFormat(f PolicyFormat) Option {
	return func(c *Config) { c.PolicyFormat = f }
}
func WithDescription(desc string) Option {
	return func(c *Config) { c.Description = desc }
}
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex is the dyn op. Internally it wraps an exec.Vertex and adds policy
// metadata. At marshal time it delegates to the inner exec, producing an
// identical wire representation. The policy is read post-solve via
// PolicyReader, injected by the solver subsystem.
type Vertex struct {
	config Config
	inner  *execop.Vertex
	cache  marshal.Cache
}

// New constructs a dyn vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{
		PolicyFormat: PolicyFormatRego,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.PolicyPath == "" {
		return nil, fmt.Errorf("dyn.New: PolicyPath is required")
	}

	// Build the inner exec vertex.
	allExecOpts := make([]execop.Option, len(cfg.ExecOpts))
	copy(allExecOpts, cfg.ExecOpts)
	// Add a writable mount for the policy output directory.
	allExecOpts = append(allExecOpts, execop.WithMount(execop.Mount{
		Target:   "/output",
		Type:     execop.MountTypeBind,
		ReadOnly: false,
	}))
	inner, err := execop.New(allExecOpts...)
	if err != nil {
		return nil, fmt.Errorf("dyn.New: inner exec: %w", err)
	}
	return &Vertex{config: cfg, inner: inner}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeDyn }

func (v *Vertex) Inputs() []core.Edge { return v.inner.Inputs() }

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{
		{Index: 0, Description: "dyn exec filesystem result"},
		{Index: 1, Description: "policy output"},
	}
}

func (v *Vertex) Validate(ctx context.Context, c *core.Constraints) error {
	if v.config.PolicyPath == "" {
		return &core.ValidationError{Field: "PolicyPath", Cause: fmt.Errorf("must not be empty")}
	}
	return v.inner.Validate(ctx, c)
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	// Delegate to the inner exec vertex for the actual serialisation.
	mv, err := v.inner.Marshal(ctx, c)
	if err != nil {
		return nil, &core.DynEvalError{Cause: fmt.Errorf("marshal inner exec: %w", err)}
	}

	// Re-wrap with dyn-specific metadata by deserialising, adding attrs, and
	// re-serialising so that the dyn vertex has a distinct digest.
	cfg := &v.config
	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)

	// Parse the inner op and overlay dyn metadata.
	var innerOp pb.Op
	if err := innerOp.UnmarshalVT(mv.Bytes); err != nil {
		return nil, &core.DynEvalError{Cause: fmt.Errorf("unmarshal inner op: %w", err)}
	}

	// Borrow inputs and the exec payload from the inner op.
	pop.Inputs = innerOp.Inputs
	pop.Op = innerOp.Op

	// Inject dyn metadata as description attributes.
	if md.Description == nil {
		md.Description = make(map[string]string)
	}
	md.Description["dyn.policy.path"] = cfg.PolicyPath
	md.Description["dyn.policy.format"] = string(cfg.PolicyFormat)
	if cfg.Description != "" {
		md.Description["llb.customname"] = cfg.Description
	}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, &core.DynEvalError{Cause: fmt.Errorf("marshal: %w", err)}
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	newInner, err := v.inner.WithInputs(inputs)
	if err != nil {
		return nil, err
	}
	return &Vertex{
		config: v.config,
		inner:  newInner.(*execop.Vertex),
	}, nil
}

// Output returns a core.Output for the filesystem result (slot 0).
func (v *Vertex) Output() core.Output { return &core.SimpleOutput{V: v, Slot: 0} }

// PolicyOutput returns a core.Output for the policy output (slot 1).
func (v *Vertex) PolicyOutput() core.Output { return &core.SimpleOutput{V: v, Slot: 1} }

// Inner returns the underlying exec vertex.
func (v *Vertex) Inner() *execop.Vertex { return v.inner }

// Config returns a copy of the configuration.
func (v *Vertex) Config() Config { return v.config }

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
