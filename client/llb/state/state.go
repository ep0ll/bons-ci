// Package state provides an immutable, composable fluent API over the llbx
// vertex graph, similar to BuildKit's llb.State but fully decoupled from any
// concrete vertex implementation.
package state

import (
	"context"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/bons/bons-ci/client/llb/ops/diff"
	execop "github.com/bons/bons-ci/client/llb/ops/exec"
	fileop "github.com/bons/bons-ci/client/llb/ops/file"
	mergeop "github.com/bons/bons-ci/client/llb/ops/merge"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Scratch ─────────────────────────────────────────────────────────────────

// Scratch returns an empty-filesystem State.
func Scratch() State { return State{} }

// ─── State ───────────────────────────────────────────────────────────────────

// State is an immutable reference to a point in the build graph.
// A zero-value State is scratch (empty filesystem).
type State struct {
	output   core.Output
	platform *ocispecs.Platform // propagated from source ops
}

// From wraps an existing core.Output in a State.
func From(out core.Output) State {
	s := State{output: out}
	s = s.extractPlatform()
	return s
}

// Output returns the underlying core.Output (nil for scratch).
func (s State) Output() core.Output { return s.output }

// IsScratch reports whether the state is empty (nil output).
func (s State) IsScratch() bool { return s.output == nil }

// GetPlatform returns the platform propagated from the backing source op, or nil.
func (s State) GetPlatform() *ocispecs.Platform { return s.platform }

// ─── File ─────────────────────────────────────────────────────────────────────

// File applies a file op vertex to this state and returns the result.
func (s State) File(v *fileop.Vertex) State { return From(v.Output()) }

// ─── Run ─────────────────────────────────────────────────────────────────────

// Run executes a command on top of this state (as root filesystem) and returns
// an ExecState that gives access to individual mount outputs.
func (s State) Run(v *execop.Vertex) ExecState {
	return ExecState{root: From(v.RootOutput()), exec: v}
}

// ─── Merge ───────────────────────────────────────────────────────────────────

// Merge overlays this state with others. Scratch inputs are silently dropped.
// Returns Scratch if fewer than 2 non-scratch inputs remain.
func (s State) Merge(others ...State) State {
	inputs := make([]core.Output, 0, 1+len(others))
	if s.output != nil {
		inputs = append(inputs, s.output)
	}
	for _, o := range others {
		if o.output != nil {
			inputs = append(inputs, o.output)
		}
	}
	switch len(inputs) {
	case 0:
		return Scratch()
	case 1:
		return From(inputs[0])
	}
	v, err := mergeop.New(mergeop.WithInputs(inputs...))
	if err != nil {
		return Scratch()
	}
	return From(v.Output())
}

// ─── Diff ─────────────────────────────────────────────────────────────────────

// Diff computes the filesystem delta between this state (lower) and upper.
func (s State) Diff(upper State) State {
	return From(diff.New(diff.WithLower(s.output), diff.WithUpper(upper.output)).Output())
}

// ─── Marshal ─────────────────────────────────────────────────────────────────

// Marshal serialises the build graph rooted at this state.
func (s State) Marshal(ctx context.Context, opts ...core.ConstraintsOption) (*marshal.Definition, error) {
	if s.output == nil {
		return &marshal.Definition{
			Metadata: make(map[core.VertexID]core.OpMetadata),
		}, nil
	}
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, opts...)

	v := s.output.Vertex(ctx, c)
	if v == nil {
		return &marshal.Definition{
			Metadata: make(map[core.VertexID]core.OpMetadata),
		}, nil
	}
	return marshal.NewSerializer().Serialize(ctx, s.output, c)
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (s State) extractPlatform() State {
	if s.output == nil {
		return s
	}
	type platformProvider interface {
		Platform() *ocispecs.Platform
	}
	if pp, ok := s.output.(platformProvider); ok {
		s.platform = pp.Platform()
	}
	return s
}

// ─── ExecState ───────────────────────────────────────────────────────────────

// ExecState wraps the result of Run(), exposing root and named mount outputs.
type ExecState struct {
	root State
	exec *execop.Vertex
}

// Root returns the State representing the root filesystem after execution.
func (e ExecState) Root() State { return e.root }

// GetMount returns the State for the specified mount target path.
// Returns Scratch if the mount is not found or has no writable output.
func (e ExecState) GetMount(target string) State {
	out := e.exec.OutputFor(target)
	if out == nil {
		return Scratch()
	}
	return From(out)
}
