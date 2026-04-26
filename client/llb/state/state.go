// Package state provides an immutable, composable fluent API over the llb
// vertex graph, mirroring BuildKit's llb.State with full metadata value
// propagation, async lazy evaluation, and complete decoupling from any
// concrete vertex implementation.
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/bons/bons-ci/client/llb/ops/diff"
	execop "github.com/bons/bons-ci/client/llb/ops/exec"
	"github.com/bons/bons-ci/client/llb/ops/export"
	fileop "github.com/bons/bons-ci/client/llb/ops/file"
	mergeop "github.com/bons/bons-ci/client/llb/ops/merge"
	"github.com/bons/bons-ci/client/llb/ops/solve"
	"github.com/moby/buildkit/solver/pb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Scratch ─────────────────────────────────────────────────────────────────

// Scratch returns an empty-filesystem State.
func Scratch() State { return State{} }

// ─── State ───────────────────────────────────────────────────────────────────

// State is an immutable reference to a point in the build graph.
// A zero-value State is scratch (empty filesystem).
//
// States carry a key-value metadata chain that preserves environment variables,
// working directory, user, hostname, platform, network mode, security mode, and
// other exec-time settings. Each mutation returns a new State with the previous
// as its implicit parent, forming an immutable linked list of metadata.
//
// Operations performed on a State are lazily evaluated at marshal time.
type State struct {
	output core.Output
	prev   *State                                                              // metadata parent
	key    any                                                                 // metadata key
	value  func(ctx context.Context, c *core.Constraints) (any, error) // metadata value thunk
}

// From wraps an existing core.Output in a State.
func From(out core.Output) State {
	return State{output: out}
}

// NewState creates a State from an Output (alias for From, BuildKit compat).
func NewState(out core.Output) State {
	return From(out)
}

// Output returns the underlying core.Output (nil for scratch).
func (s State) Output() core.Output { return s.output }

// IsScratch reports whether the state is empty (nil output).
func (s State) IsScratch() bool { return s.output == nil }

// WithOutput creates a new State with the output replaced.
func (s State) WithOutput(o core.Output) State {
	ns := s
	ns.output = o
	return ns
}

// ─── Value chain ─────────────────────────────────────────────────────────────

// WithValue returns a new State with a simple value set on the metadata chain.
func (s State) WithValue(k, v any) State {
	return s.withValue(k, func(context.Context, *core.Constraints) (any, error) {
		return v, nil
	})
}

// withValue is the internal method that stores a lazy thunk on the chain.
func (s State) withValue(k any, v func(context.Context, *core.Constraints) (any, error)) State {
	return State{
		output: s.output,
		prev:   &s,
		key:    k,
		value:  v,
	}
}

// Value retrieves a metadata value by walking the chain.
func (s State) Value(ctx context.Context, k any, c *core.Constraints) (any, error) {
	return s.getValue(k)(ctx, c)
}

func (s State) getValue(k any) func(context.Context, *core.Constraints) (any, error) {
	if s.key == k {
		return s.value
	}
	if s.prev != nil {
		return s.prev.getValue(k)
	}
	return func(context.Context, *core.Constraints) (any, error) {
		return nil, nil
	}
}

// ─── With (StateOption composition) ──────────────────────────────────────────

// With applies StateOptions to the State.
// Each applied StateOption creates a new State with the previous as its parent.
func (s State) With(so ...StateOption) State {
	for _, o := range so {
		s = o(s)
	}
	return s
}

// ─── Platform ────────────────────────────────────────────────────────────────

// Platform sets the platform for the state.
func (s State) Platform(p ocispecs.Platform) State {
	return s.With(setPlatform(p))
}

// GetPlatform returns the platform for the state.
func (s State) GetPlatform(ctx context.Context, co ...core.ConstraintsOption) (*ocispecs.Platform, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	return getPlatform(s)(ctx, c)
}

// ─── Environment ─────────────────────────────────────────────────────────────

// AddEnv returns a new State with the provided environment variable set.
func (s State) AddEnv(key, value string) State {
	return s.With(AddEnv(key, value))
}

// AddEnvf is the same as AddEnv but with a format string.
func (s State) AddEnvf(key, value string, v ...any) State {
	return s.With(AddEnvf(key, value, v...))
}

// DelEnv returns a new State with the environment variable removed.
func (s State) DelEnv(key string) State {
	return s.With(DelEnv(key))
}

// GetEnv returns the value of the environment variable with the provided key.
func (s State) GetEnv(ctx context.Context, key string, co ...core.ConstraintsOption) (string, bool, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	env, err := getEnv(s)(ctx, c)
	if err != nil {
		return "", false, err
	}
	v, ok := env.Get(key)
	return v, ok, nil
}

// Env returns the current environment variable list.
func (s State) Env(ctx context.Context, co ...core.ConstraintsOption) (*EnvList, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	return getEnv(s)(ctx, c)
}

// ─── Directory ───────────────────────────────────────────────────────────────

// Dir returns a new State with the provided working directory set.
func (s State) Dir(str string) State {
	return s.With(Dir(str))
}

// Dirf is the same as Dir but with a format string.
func (s State) Dirf(str string, v ...any) State {
	return s.With(Dirf(str, v...))
}

// GetDir returns the current working directory for the state.
func (s State) GetDir(ctx context.Context, co ...core.ConstraintsOption) (string, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	return getDir(s)(ctx, c)
}

// GetArgs returns the current args for the state.
func (s State) GetArgs(ctx context.Context, co ...core.ConstraintsOption) ([]string, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	return getArgs(s)(ctx, c)
}

// ─── User ────────────────────────────────────────────────────────────────────

// User sets the user for this state.
func (s State) User(v string) State {
	return s.With(User(v))
}

// ─── Hostname ────────────────────────────────────────────────────────────────

// Hostname sets the hostname for this state.
func (s State) Hostname(v string) State {
	return s.With(Hostname(v))
}

// GetHostname returns the hostname set on the state.
func (s State) GetHostname(ctx context.Context, co ...core.ConstraintsOption) (string, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	return getHostname(s)(ctx, c)
}

// ─── Reset ───────────────────────────────────────────────────────────────────

// Reset creates a new State with the output of the current State and the
// provided State as the metadata parent.
func (s State) Reset(s2 State) State {
	return s.With(Reset(s2))
}

// ─── Network ─────────────────────────────────────────────────────────────────

// Network sets the network mode for the state.
func (s State) Network(n pb.NetMode) State {
	return s.With(Network(n))
}

// GetNetwork returns the network mode for the state.
func (s State) GetNetwork(ctx context.Context, co ...core.ConstraintsOption) (pb.NetMode, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	return getNetwork(s)(ctx, c)
}

// ─── Security ────────────────────────────────────────────────────────────────

// Security sets the security mode for the state.
func (s State) Security(n pb.SecurityMode) State {
	return s.With(Security(n))
}

// GetSecurity returns the security mode for the state.
func (s State) GetSecurity(ctx context.Context, co ...core.ConstraintsOption) (pb.SecurityMode, error) {
	c := core.DefaultConstraints()
	core.ApplyConstraintsOptions(c, co...)
	return getSecurity(s)(ctx, c)
}

// ─── Extra hosts / Ulimits / Cgroup ──────────────────────────────────────────

// AddExtraHost adds a host name to IP mapping.
func (s State) AddExtraHost(host string, ip net.IP) State {
	return s.With(extraHost(host, ip))
}

// AddUlimit sets a ulimit.
func (s State) AddUlimit(name UlimitName, soft, hard int64) State {
	return s.With(ulimit(name, soft, hard))
}

// WithCgroupParent sets the parent cgroup.
func (s State) WithCgroupParent(cp string) State {
	return s.With(cgroupParent(cp))
}

// ─── WithImageConfig ─────────────────────────────────────────────────────────

// WithImageConfig adds the environment variables, working directory, and
// platform specified in an OCI image config to the state.
func (s State) WithImageConfig(dt []byte) (State, error) {
	type imgConfig struct {
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
		User       string   `json:"User"`
	}
	type img struct {
		Config imgConfig `json:"config"`
	}
	var m img
	if err := json.Unmarshal(dt, &m); err != nil {
		return State{}, fmt.Errorf("WithImageConfig: %w", err)
	}
	for _, envKV := range m.Config.Env {
		parts := strings.SplitN(envKV, "=", 2)
		if len(parts) == 2 {
			s = s.AddEnv(parts[0], parts[1])
		}
	}
	if m.Config.WorkingDir != "" {
		s = s.Dir(m.Config.WorkingDir)
	}
	if m.Config.User != "" {
		s = s.User(m.Config.User)
	}
	return s, nil
}

// ─── SetMarshalDefaults ──────────────────────────────────────────────────────

// SetMarshalDefaults returns a new State with default constraints set for marshalling.
func (s State) SetMarshalDefaults(co ...core.ConstraintsOption) State {
	return s.WithValue(contextKeyT("llb.marshaldefaults"), co)
}

// ─── Async ───────────────────────────────────────────────────────────────────

// Async returns a new State that lazily evaluates the provided function
// at marshal time. The function receives the current State and constraints.
func (s State) Async(f func(context.Context, State, *core.Constraints) (State, error)) State {
	as := &asyncState{f: f, prev: s}
	return State{
		output: as,
		prev:   &s,
	}
}

// ─── File ────────────────────────────────────────────────────────────────────

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

// ─── Diff ────────────────────────────────────────────────────────────────────

// Diff computes the filesystem delta between this state (lower) and upper.
func (s State) Diff(upper State) State {
	return From(diff.New(diff.WithLower(s.output), diff.WithUpper(upper.output)).Output())
}

// ─── Solve ───────────────────────────────────────────────────────────────────

// Solve wraps this state's sub-graph as a SolveOp vertex.
func (s State) Solve(opts ...solve.Option) State {
	if s.output == nil {
		return Scratch()
	}
	allOpts := append([]solve.Option{solve.WithInput(s.output)}, opts...)
	v, err := solve.New(allOpts...)
	if err != nil {
		return Scratch()
	}
	return From(v.Output())
}

// ─── Export ──────────────────────────────────────────────────────────────────

// Export declares an export target for this state's output.
func (s State) Export(opts ...export.Option) State {
	if s.output == nil {
		return Scratch()
	}
	allOpts := append([]export.Option{export.WithInput(s.output)}, opts...)
	v, err := export.New(allOpts...)
	if err != nil {
		return Scratch()
	}
	return From(v.Output())
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

// ─── Validate ────────────────────────────────────────────────────────────────

// Validate validates the state eagerly. Unlike most State operations,
// this is not lazy.
func (s State) Validate(ctx context.Context, c *core.Constraints) error {
	if s.output == nil {
		return nil
	}
	v := s.output.Vertex(ctx, c)
	if v == nil {
		return nil
	}
	return v.Validate(ctx, c)
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

// ─── Async state implementation ──────────────────────────────────────────────

type asyncState struct {
	f    func(context.Context, State, *core.Constraints) (State, error)
	prev State
	once sync.Once
	res  State
	err  error
}

func (as *asyncState) Vertex(ctx context.Context, c *core.Constraints) core.Vertex {
	target, err := as.do(ctx, c)
	if err != nil {
		return &errVertex{err: err}
	}
	out := target.Output()
	if out == nil {
		return nil
	}
	return out.Vertex(ctx, c)
}

func (as *asyncState) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	target, err := as.do(ctx, c)
	if err != nil {
		return nil, err
	}
	out := target.Output()
	if out == nil {
		return nil, nil
	}
	return out.ToInput(ctx, c)
}

func (as *asyncState) do(ctx context.Context, c *core.Constraints) (State, error) {
	as.once.Do(func() {
		as.res, as.err = as.f(ctx, as.prev, c)
	})
	return as.res, as.err
}

// ─── errVertex ───────────────────────────────────────────────────────────────

type errVertex struct {
	err error
}

func (v *errVertex) Type() core.VertexType                                     { return "error" }
func (v *errVertex) Inputs() []core.Edge                                       { return nil }
func (v *errVertex) Outputs() []core.OutputSlot                                { return nil }
func (v *errVertex) Validate(context.Context, *core.Constraints) error         { return v.err }
func (v *errVertex) Marshal(context.Context, *core.Constraints) (*core.MarshaledVertex, error) {
	return nil, v.err
}
func (v *errVertex) WithInputs([]core.Edge) (core.Vertex, error) { return nil, v.err }

var _ core.Vertex = (*errVertex)(nil)

// ─── CopyInput interface (for file op compat) ────────────────────────────────

func (State) isFileOpCopyInput() {}
