// Package state provides a high-level, fluent builder API over llbx's vertex
// graph. It bridges the low-level vertex/output primitives with an ergonomic
// interface that mirrors the BuildKit llb.State concept while remaining fully
// decoupled from any specific vertex implementation.
//
// Immutability contract
// ─────────────────────
// Every method on State returns a new State; the receiver is never modified.
// States are therefore safe to share across goroutines and to use as
// immutable checkpoints in a build definition.
//
// Usage
//
//	alpine := state.From(image.Must(image.WithRef("alpine:3.20")).Output())
//	built  := alpine.
//	    File(file.New(file.OnState(alpine.Output()), file.Do(file.Mkfile("/hello", 0644, []byte("hi"))))).
//	    Run(exec.New(exec.WithCommand("cat", "/hello"), exec.WithWorkingDir("/")))
package state

import (
	"context"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/bons/bons-ci/client/llb/ops/diff"
	execop "github.com/bons/bons-ci/client/llb/ops/exec"
	fileop "github.com/bons/bons-ci/client/llb/ops/file"
	"github.com/bons/bons-ci/client/llb/ops/merge"
	"github.com/moby/buildkit/solver/pb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Scratch ──────────────────────────────────────────────────────────────────

// Scratch returns a State representing an empty filesystem.
func Scratch() State { return State{} }

// ─── State ────────────────────────────────────────────────────────────────────

// State is an immutable reference to a point in the build graph, identified by
// a core.Output. A zero-value State represents scratch (an empty filesystem).
type State struct {
	output core.Output

	// platform is the resolved platform for this state, used to propagate
	// platform information to derived operations.
	platform *ocispecs.Platform
}

// From wraps an existing core.Output in a State.
func From(out core.Output) State {
	s := State{output: out}
	s = s.extractPlatform()
	return s
}

// Output returns the underlying core.Output, or nil for scratch.
func (s State) Output() core.Output { return s.output }

// IsScratch reports whether the state represents an empty filesystem.
func (s State) IsScratch() bool { return s.output == nil }

// ─── File operations ─────────────────────────────────────────────────────────

// File applies a file op vertex and returns the resulting State.
func (s State) File(v *fileop.Vertex) State {
	return From(v.Output())
}

// ─── Exec / Run ───────────────────────────────────────────────────────────────

// Run executes a command on top of this state (as the root filesystem) and
// returns an ExecState that exposes individual mount outputs.
func (s State) Run(v *execop.Vertex) ExecState {
	return ExecState{root: From(v.RootOutput()), exec: v}
}

// ─── Merge ────────────────────────────────────────────────────────────────────

// Merge overlays this state with others and returns the combined result.
// Scratch inputs are silently dropped. Returns Scratch if fewer than 2 real
// inputs remain.
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
	if len(inputs) < 2 {
		if len(inputs) == 1 {
			return From(inputs[0])
		}
		return Scratch()
	}
	v, err := merge.New(merge.WithInputs(inputs...))
	if err != nil {
		// Unreachable if len(inputs) >= 2.
		return Scratch()
	}
	return From(v.Output())
}

// ─── Diff ─────────────────────────────────────────────────────────────────────

// Diff computes the filesystem delta between this state (lower/base) and upper.
func (s State) Diff(upper State) State {
	return From(diff.New(diff.WithLower(s.output), diff.WithUpper(upper.output)).Output())
}

// ─── Serialisation ────────────────────────────────────────────────────────────

// Marshal serialises the build graph rooted at this state into a Definition.
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

	return marshal.NewSerializer().Serialize(ctx, v, c)
}

// ─── Platform propagation ─────────────────────────────────────────────────────

func (s State) extractPlatform() State {
	if s.output == nil {
		return s
	}
	if pp, ok := s.output.(interface{ Platform() *ocispecs.Platform }); ok {
		s.platform = pp.Platform()
	}
	return s
}

// GetPlatform returns the platform associated with this state, or nil.
func (s State) GetPlatform() *ocispecs.Platform { return s.platform }

// ─── ExecState ────────────────────────────────────────────────────────────────

// ExecState wraps the result of a Run call, providing access to individual
// mount outputs in addition to the root filesystem.
type ExecState struct {
	root State
	exec *execop.Vertex
}

// Root returns the State representing the root filesystem after the exec.
func (e ExecState) Root() State { return e.root }

// GetMount returns the State for the specified mount target path.
// Returns Scratch if the mount is not found.
func (e ExecState) GetMount(target string) State {
	out := e.exec.OutputFor(target)
	if out == nil {
		return Scratch()
	}
	return From(out)
}

// ─── scratch output helper ────────────────────────────────────────────────────

// scratchOutput is a sentinel core.Output for scratch states.
type scratchOutput struct{}

func (scratchOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return nil }
func (scratchOutput) ToInput(_ context.Context, _ *core.Constraints) (*pb.Input, error) {
	return nil, nil
}
