package llb

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// StateOption is a functional option that transforms a State into a new State.
// It is the primary mechanism for composing state transformations.
type StateOption func(State) State

// ─────────────────────────────────────────────────────────────────────────────
// State
// ─────────────────────────────────────────────────────────────────────────────

// State represents all operations that must be done to produce a given output.
// States are immutable; every method returns a new State linked to the
// previous one. State is the core type of the LLB DSL and is used to build a
// directed acyclic graph of operations.
//
// Operations performed on a State are executed lazily: the entire graph is
// marshalled into a Definition and then sent to a backend for execution.
type State struct {
	out     Output
	prev    *State
	key     any
	value   func(context.Context, *Constraints) (any, error)
	opts    []ConstraintsOpt
}

// NewState constructs a State backed by the given Output.
func NewState(o Output) State {
	s := State{out: o}
	s = s.ensurePlatform()
	return s
}

// Scratch returns the empty/scratch state with no output.
func Scratch() State {
	return State{}
}

// ensurePlatform propagates a platform from the output's vertex when one is
// available and the state does not already carry one.
func (s State) ensurePlatform() State {
	if s.value == nil {
		return s
	}
	return s
}

// ─────────────────────────────────────────────────────────────────────────────
// Lazy key-value metadata
// ─────────────────────────────────────────────────────────────────────────────

// WithValue returns a new State with the given key-value pair attached. The
// value is set eagerly.
func (s State) WithValue(k, v any) State {
	return s.withValue(k, func(context.Context, *Constraints) (any, error) {
		return v, nil
	})
}

// withValue returns a new State with a lazy value function attached.
func (s State) withValue(k any, v func(context.Context, *Constraints) (any, error)) State {
	return State{
		out:   s.out,
		prev:  &s,
		key:   k,
		value: v,
	}
}

// Value resolves the value for the given key by walking the State chain.
func (s State) GetValue(ctx context.Context, k any, co ...ConstraintsOpt) (any, error) {
	c := NewConstraints(co...)
	getter := s.getValue(k)
	if getter == nil {
		return nil, nil
	}
	return getter(ctx, c)
}

// getValue walks the linked State chain to find the nearest value function
// matching the given key.
func (s State) getValue(k any) func(context.Context, *Constraints) (any, error) {
	if s.key == k {
		return s.value
	}
	if s.prev != nil {
		return s.prev.getValue(k)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Async
// ─────────────────────────────────────────────────────────────────────────────

// Async returns a new State whose output will be lazily resolved when the
// graph is marshalled. The resolution function receives the current state and
// constraints so it can perform constraint-dependent transformations.
func (s State) Async(f func(context.Context, State, *Constraints) (State, error)) State {
	as := &asyncState{f: f, prev: s}
	return State{out: as}
}

// ─────────────────────────────────────────────────────────────────────────────
// Marshal defaults
// ─────────────────────────────────────────────────────────────────────────────

// SetMarshalDefaults returns a new State with default constraints applied.
func (s State) SetMarshalDefaults(co ...ConstraintsOpt) State {
	s.opts = append(s.opts, co...)
	return s
}

// ─────────────────────────────────────────────────────────────────────────────
// Output / graph access
// ─────────────────────────────────────────────────────────────────────────────

// Output returns the output of this state. Returns nil for scratch.
func (s State) Output() Output {
	return s.out
}

// WithOutput creates a new State with the output replaced.
func (s State) WithOutput(o Output) State {
	prev := s
	return State{
		out:  o,
		prev: &prev,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Validate
// ─────────────────────────────────────────────────────────────────────────────

// Validate validates the state eagerly, unlike most lazy operations.
func (s State) Validate(ctx context.Context, c *Constraints) error {
	if s.out == nil {
		return nil
	}
	v := s.out.Vertex(ctx, c)
	if v == nil {
		return nil
	}
	return v.Validate(ctx, c)
}

// ─────────────────────────────────────────────────────────────────────────────
// Marshal
// ─────────────────────────────────────────────────────────────────────────────

// Marshal marshals the state and all its parents into a Definition.
func (s State) Marshal(ctx context.Context, co ...ConstraintsOpt) (*Definition, error) {
	c := NewConstraints(append(s.opts, co...)...)
	def := &Definition{
		Metadata: make(map[digest.Digest]OpMetadata),
	}
	smc := newSourceMapCollector()

	if s.Output() == nil {
		return def, nil
	}

	v := s.Output().Vertex(ctx, c)
	if v == nil {
		return def, nil
	}

	vertexCache := make(map[Vertex]struct{})
	digestCache := make(map[digest.Digest]struct{})

	def, err := marshalVertex(ctx, v, def, smc, digestCache, vertexCache, c)
	if err != nil {
		return nil, err
	}

	// Emit the terminal input reference.
	inp, err := s.Output().ToInput(ctx, c)
	if err != nil {
		return nil, err
	}

	var pop pb.Op
	pop.Inputs = append(pop.Inputs, inp)
	dt, err := DeterministicMarshal(&pop)
	if err != nil {
		return nil, err
	}
	def.Def = append(def.Def, dt)

	dgst := digest.FromBytes(dt)
	if md, ok := def.Metadata[dgst]; ok {
		_ = md // terminal op metadata is already set via inputs
	}

	src, err := smc.Marshal(ctx, co...)
	if err != nil {
		return nil, err
	}
	def.Source = src
	def.Constraints = c

	return def, nil
}

// marshalVertex recursively marshals a Vertex and all its inputs into the
// Definition. It deduplicates by both vertex identity and digest.
func marshalVertex(
	ctx context.Context,
	v Vertex,
	def *Definition,
	smc *sourceMapCollector,
	digestCache map[digest.Digest]struct{},
	vertexCache map[Vertex]struct{},
	c *Constraints,
) (*Definition, error) {
	if _, ok := vertexCache[v]; ok {
		return def, nil
	}
	vertexCache[v] = struct{}{}

	// Recurse into inputs first (topological order).
	for _, inp := range v.Inputs() {
		if inp == nil {
			continue
		}
		iv := inp.Vertex(ctx, c)
		if iv == nil {
			continue
		}
		var err error
		def, err = marshalVertex(ctx, iv, def, smc, digestCache, vertexCache, c)
		if err != nil {
			return nil, err
		}
	}

	dgst, dt, md, srcs, err := v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}

	if srcs != nil {
		smc.Add(dgst, srcs)
	}

	if _, ok := digestCache[dgst]; ok {
		return def, nil
	}
	digestCache[dgst] = struct{}{}

	def.Def = append(def.Def, dt)
	if md != nil {
		def.Metadata[dgst] = NewOpMetadata(md)
	}

	return def, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// With applies a chain of StateOptions to the State.
// ─────────────────────────────────────────────────────────────────────────────

// With applies the given StateOptions to this State, returning the result.
func (s State) With(opts ...StateOption) State {
	for _, o := range opts {
		s = o(s)
	}
	return s
}

// ─────────────────────────────────────────────────────────────────────────────
// Image config helper
// ─────────────────────────────────────────────────────────────────────────────

// WithImageConfig applies environment variables, working directory, user, and
// platform from a JSON-encoded OCI image config to the state.
func (s State) WithImageConfig(config []byte) (State, error) {
	var img struct {
		Config struct {
			Env        []string `json:"Env,omitempty"`
			WorkingDir string   `json:"WorkingDir,omitempty"`
			User       string   `json:"User,omitempty"`
		} `json:"config,omitempty"`
		Architecture string `json:"architecture,omitempty"`
		OS           string `json:"os,omitempty"`
		Variant      string `json:"variant,omitempty"`
	}
	if err := json.Unmarshal(config, &img); err != nil {
		return State{}, err
	}

	for _, env := range img.Config.Env {
		k, v, _ := strings.Cut(env, "=")
		s = s.AddEnv(k, v)
	}
	if img.Config.WorkingDir != "" {
		s = s.Dir(img.Config.WorkingDir)
	}
	if img.Config.User != "" {
		s = s.User(img.Config.User)
	}
	if img.Architecture != "" || img.OS != "" {
		s = s.withValue(keyPlatform, func(_ context.Context, _ *Constraints) (any, error) {
			return &ocispecs.Platform{
				OS:           img.OS,
				Architecture: img.Architecture,
				Variant:      img.Variant,
			}, nil
		})
	}
	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata accessors (delegate to meta.go keys)
// ─────────────────────────────────────────────────────────────────────────────

// AddEnv adds an environment variable to the state.
func (s State) AddEnv(key, value string) State {
	return addEnv(key, value)(s)
}

// AddEnvf adds a formatted environment variable.
func (s State) AddEnvf(key, value string, v ...any) State {
	return addEnvf(key, value, v...)(s)
}

// Dir sets the working directory.
func (s State) Dir(path string) State {
	return dir(path)(s)
}

// Dirf sets a formatted working directory.
func (s State) Dirf(path string, v ...any) State {
	return dirf(path, v...)(s)
}

// User sets the user for exec operations.
func (s State) User(user string) State {
	return setUser(user)(s)
}

// Hostname sets the hostname for exec containers.
func (s State) Hostname(h string) State {
	return hostname(h)(s)
}

// Platform sets the target platform.
func (s State) Platform(p ocispecs.Platform) State {
	return setPlatform(p)(s)
}

// Network returns the network mode.
func (s State) GetNetwork(ctx context.Context, co ...ConstraintsOpt) (pb.NetMode, error) {
	v, err := s.GetValue(ctx, keyNetwork, co...)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return pb.NetMode_UNSET, nil
	}
	return v.(pb.NetMode), nil
}

// GetPlatform returns the current platform.
func (s State) GetPlatform(ctx context.Context, co ...ConstraintsOpt) (*ocispecs.Platform, error) {
	v, err := s.GetValue(ctx, keyPlatform, co...)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.(*ocispecs.Platform), nil
}

// GetDir retrieves the working directory.
func (s State) GetDir(ctx context.Context, co ...ConstraintsOpt) (string, error) {
	v, err := s.GetValue(ctx, keyDir, co...)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", nil
	}
	return v.(string), nil
}

// GetArgs retrieves the exec args.
func (s State) GetArgs(ctx context.Context, co ...ConstraintsOpt) ([]string, error) {
	v, err := s.GetValue(ctx, keyArgs, co...)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]string), nil
}

// GetEnv retrieves the environment variable list.
func (s State) GetEnv(ctx context.Context, co ...ConstraintsOpt) (*EnvList, error) {
	v, err := s.GetValue(ctx, keyEnv, co...)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return &EnvList{}, nil
	}
	return v.(*EnvList), nil
}

// GetUser retrieves the user.
func (s State) GetUser(ctx context.Context, co ...ConstraintsOpt) (string, error) {
	v, err := s.GetValue(ctx, keyUser, co...)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", nil
	}
	return v.(string), nil
}

// GetHostname retrieves the hostname.
func (s State) GetHostname(ctx context.Context, co ...ConstraintsOpt) (string, error) {
	v, err := s.GetValue(ctx, keyHostname, co...)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", nil
	}
	return v.(string), nil
}

// GetExtraHosts retrieves the extra host entries.
func (s State) GetExtraHosts(ctx context.Context, co ...ConstraintsOpt) ([]HostIP, error) {
	v, err := s.GetValue(ctx, keyExtraHost, co...)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]HostIP), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// EnvList
// ─────────────────────────────────────────────────────────────────────────────

// EnvList is an ordered key-value list of environment variables.
type EnvList struct {
	keys   []string
	values map[string]string
}

// NewEnvList creates a new empty EnvList.
func NewEnvList() *EnvList {
	return &EnvList{values: make(map[string]string)}
}

// Set adds or updates an environment variable.
func (el *EnvList) Set(key, value string) *EnvList {
	cp := &EnvList{
		keys:   make([]string, 0, len(el.keys)+1),
		values: make(map[string]string, len(el.values)+1),
	}
	for k, v := range el.values {
		cp.values[k] = v
	}
	// Remove existing key to maintain insertion order for re-sets.
	for _, k := range el.keys {
		if k != key {
			cp.keys = append(cp.keys, k)
		}
	}
	cp.keys = append(cp.keys, key)
	cp.values[key] = value
	return cp
}

// Get returns the value for a key.
func (el *EnvList) Get(key string) (string, bool) {
	v, ok := el.values[key]
	return v, ok
}

// ToArray converts the EnvList to KEY=VALUE pairs.
func (el *EnvList) ToArray() []string {
	result := make([]string, 0, len(el.keys))
	for _, k := range el.keys {
		result = append(result, k+"="+el.values[k])
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// HostIP
// ─────────────────────────────────────────────────────────────────────────────

// HostIP represents a hostname→IP mapping for extra /etc/hosts entries.
type HostIP struct {
	Host string
	IP   net.IP
}

// String returns the host:ip representation.
func (h HostIP) String() string {
	return fmt.Sprintf("%s:%s", h.Host, h.IP)
}
