// Package state provides a high-level, chainable API for constructing DAG vertices.
//
// State is the primary user-facing type. It wraps a vertex.Ref and carries
// inherited metadata (environment variables, working directory, platform, etc.)
// as a persistent linked list. Every State operation returns a new State —
// States are immutable.
package state

import (
	"context"
	"path"

	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// State is an immutable snapshot of a filesystem reference plus execution
// metadata that provides context for subsequent operations.
type State struct {
	ref  vertex.Ref
	meta *Meta
}

// Scratch returns a State representing an empty filesystem.
func Scratch() State {
	return State{ref: vertex.Ref{}, meta: defaultMeta()}
}

// FromRef wraps an existing vertex.Ref in a State with default metadata.
func FromRef(ref vertex.Ref) State {
	return State{ref: ref, meta: defaultMeta()}
}

// Ref returns the underlying vertex reference.
func (s State) Ref() vertex.Ref { return s.ref }

// Output returns the Vertex that produces this State's filesystem.
func (s State) Output() vertex.Vertex {
	if s.ref.IsZero() {
		return nil
	}
	return s.ref.Vertex
}

// ─── Source constructors ──────────────────────────────────────────────────────

func Image(ref string, opts ...func(*ops.ImageInfo)) State {
	src := ops.Image(ref, opts...)
	return State{ref: src.Ref(), meta: defaultMeta()}
}

func Git(url string, opts ...func(*ops.GitInfo)) State {
	return State{ref: ops.Git(url, opts...).Ref(), meta: defaultMeta()}
}

func HTTP(url string, opts ...func(*ops.HTTPInfo)) State {
	return State{ref: ops.HTTP(url, opts...).Ref(), meta: defaultMeta()}
}

func Local(name string, opts ...func(*ops.LocalInfo)) State {
	return State{ref: ops.Local(name, opts...).Ref(), meta: defaultMeta()}
}

// ─── Run ──────────────────────────────────────────────────────────────────────

// RunState wraps an ExecOp and provides access to named mount outputs.
type RunState struct {
	State // embedded: the root filesystem output of the exec
	exec  *ops.ExecOp
}

// Run executes a command on this State's filesystem and returns a RunState.
// The RunState's embedded State represents the root filesystem after execution.
// Metadata (dir, env, user, network) is propagated from this State to the exec
// unless explicitly overridden via ExecOption arguments.
func (s State) Run(execOpts ...ops.ExecOption) RunState {
	m := s.ensureMeta()

	// Hoist State metadata into the exec. Caller-provided execOpts are applied
	// last and may override any of these.
	var allOpts []ops.ExecOption
	cwd := m.dir
	if cwd == "" {
		cwd = "/"
	}
	allOpts = append(allOpts, ops.WithCwd(cwd))
	if env := m.env.ToSlice(); len(env) > 0 {
		allOpts = append(allOpts, ops.WithEnv(env...))
	}
	if m.user != "" {
		allOpts = append(allOpts, ops.WithUser(m.user))
	}
	if m.network != ops.NetModeSandbox {
		allOpts = append(allOpts, ops.WithNetwork(m.network))
	}
	allOpts = append(allOpts, execOpts...)

	e := ops.Exec(s.ref, allOpts...)
	rootRef := e.RootRef()
	return RunState{
		State: State{ref: rootRef, meta: m.clone()},
		exec:  e,
	}
}

// Root returns the root filesystem output of the exec as a State.
func (rs RunState) Root() State { return rs.State }

// GetMount returns a State backed by the named mount's writable output.
// Returns Scratch() if the mount target was not declared in the exec,
// is read-only, or is a cache/tmpfs mount (none of which produce output refs).
func (rs RunState) GetMount(target string) State {
	ref := rs.exec.MountRef(target)
	if ref.IsZero() {
		return Scratch()
	}
	return State{ref: ref, meta: rs.meta.clone()}
}

// AddMount declares an additional writable mount on an EXISTING exec and
// returns a State backed by that mount's output.
//
// BUG FIX: the previous implementation built a Mount struct but then read from
// the exec's *current* (pre-declaration) MountRef, always returning Scratch().
//
// The correct semantics require rebuilding the ExecOp with the new mount
// included. Since ExecOp is immutable after construction, we must create a new
// one that incorporates all previous mounts plus the new one, and return a ref
// from that new op.
//
// For adding mounts before execution, prefer passing ops.WithMount to Run().
func (rs RunState) AddMount(target string, src State, mountOpts ...func(*ops.Mount)) State {
	m := ops.Mount{Target: target, Source: src.ref}
	for _, o := range mountOpts {
		o(&m)
	}
	// Collect all existing mounts from the current exec to preserve them.
	existingMounts := rs.exec.Mounts()
	// Build a new set of mounts: existing non-root mounts + the new one.
	// The root mount (/) is the exec's source ref, handled by NewExecOp directly.
	var extraMounts []ops.Mount
	for _, em := range existingMounts {
		if em.Target == "/" {
			continue // root is passed as the first arg to NewExecOp
		}
		extraMounts = append(extraMounts, em)
	}
	extraMounts = append(extraMounts, m)

	// The root ref is the exec's first mount's source.
	var rootRef vertex.Ref
	for _, em := range existingMounts {
		if em.Target == "/" {
			rootRef = em.Source
			break
		}
	}

	newExec := ops.NewExecOp(rootRef, rs.exec.Meta(), extraMounts, rs.exec.Constraints())
	ref := newExec.MountRef(target)
	if ref.IsZero() {
		return Scratch()
	}
	return State{ref: ref, meta: rs.meta.clone()}
}

// ─── File ─────────────────────────────────────────────────────────────────────

// File applies a chain of filesystem operations to this State.
func (s State) File(action *ops.FileAction, copts ...func(*ops.Constraints)) State {
	c := ops.Constraints{}
	if s.meta != nil && s.meta.platform != nil {
		c.Platform = s.meta.platform
	}
	for _, o := range copts {
		o(&c)
	}
	f := ops.NewFileOp(s.ref, action, c)
	return State{ref: f.Ref(), meta: s.ensureMeta().clone()}
}

// ─── Merge / Diff ─────────────────────────────────────────────────────────────

// Merge overlays multiple States, returning a combined filesystem.
func Merge(inputs []State, c ops.Constraints) State {
	refs := make([]vertex.Ref, len(inputs))
	for i, s := range inputs {
		refs[i] = s.ref
	}
	ref := ops.Merge(refs, c)
	return State{ref: ref, meta: defaultMeta()}
}

// Diff computes the filesystem delta of upper relative to this State (as lower).
func (s State) Diff(upper State, c ops.Constraints) State {
	ref := ops.Diff(s.ref, upper.ref, c)
	return State{ref: ref, meta: defaultMeta()}
}

// ─── Metadata setters ─────────────────────────────────────────────────────────

// Dir returns a new State with the working directory set.
// Relative paths are resolved against the current directory.
// An empty string is a no-op (returns the State unchanged).
func (s State) Dir(str string) State {
	if str == "" {
		// BUG FIX: Dir("") previously mangled the path.
		// An empty directory string should be a no-op, not clean to ".".
		return s
	}
	m := s.ensureMeta().clone()
	if path.IsAbs(str) {
		m.dir = cleanPath(str)
	} else {
		base := m.dir
		if base == "" {
			base = "/"
		}
		m.dir = cleanPath(base + "/" + str)
	}
	return State{ref: s.ref, meta: m}
}

// GetDir returns the current working directory.
func (s State) GetDir(ctx context.Context) (string, error) {
	m := s.ensureMeta()
	if m.dir == "" {
		return "/", nil
	}
	return m.dir, nil
}

// AddEnv returns a new State with the environment variable key=value set.
// If key already exists, its value is replaced.
func (s State) AddEnv(key, value string) State {
	m := s.ensureMeta().clone()
	m.env = m.env.Set(key, value)
	return State{ref: s.ref, meta: m}
}

// DelEnv returns a new State with the environment variable key removed.
// If key does not exist, it is a no-op.
func (s State) DelEnv(key string) State {
	m := s.ensureMeta().clone()
	m.env = m.env.Del(key)
	return State{ref: s.ref, meta: m}
}

// GetEnv returns the value of an environment variable and whether it exists.
func (s State) GetEnv(key string) (string, bool) {
	return s.ensureMeta().env.Get(key)
}

// Env returns all live environment variables as KEY=VALUE strings.
func (s State) Env() []string {
	return s.ensureMeta().env.ToSlice()
}

// EnvLen returns the number of distinct environment variables currently set.
// Variables that have been deleted via DelEnv are not counted.
func (s State) EnvLen() int {
	return s.ensureMeta().env.Len()
}

// SetDefaultEnv sets key=value only if key is not already present.
// Unlike AddEnv, this is a no-op when key already exists.
// Useful for setting fallback defaults without overriding explicit values.
func (s State) SetDefaultEnv(key, value string) State {
	m := s.ensureMeta().clone()
	m.env = m.env.SetDefault(key, value)
	return State{ref: s.ref, meta: m}
}

// User returns a new State with the execution user set (UID, username, "user:group").
func (s State) User(user string) State {
	m := s.ensureMeta().clone()
	m.user = user
	return State{ref: s.ref, meta: m}
}

// GetUser returns the current execution user string.
func (s State) GetUser() string { return s.ensureMeta().user }

// Hostname returns a new State with the hostname set.
func (s State) Hostname(h string) State {
	m := s.ensureMeta().clone()
	m.hostname = h
	return State{ref: s.ref, meta: m}
}

// GetHostname returns the current hostname.
func (s State) GetHostname() string { return s.ensureMeta().hostname }

// Network returns a new State with the network mode set.
func (s State) Network(mode ops.NetMode) State {
	m := s.ensureMeta().clone()
	m.network = mode
	return State{ref: s.ref, meta: m}
}

// GetNetwork returns the current network mode.
func (s State) GetNetwork() ops.NetMode { return s.ensureMeta().network }

// Platform returns a new State with the target platform set.
func (s State) Platform(p ops.Platform) State {
	m := s.ensureMeta().clone()
	m.platform = &p
	return State{ref: s.ref, meta: m}
}

// GetPlatform returns the current target platform, or nil if unset.
func (s State) GetPlatform() *ops.Platform { return s.ensureMeta().platform }

// WithOutput creates a new State backed by the given Ref while preserving metadata.
func (s State) WithOutput(ref vertex.Ref) State {
	return State{ref: ref, meta: s.ensureMeta().clone()}
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func (s State) ensureMeta() *Meta {
	if s.meta == nil {
		return defaultMeta()
	}
	return s.meta
}

// cleanPath resolves . and .. components, always producing an absolute path.
func cleanPath(p string) string {
	result := path.Clean(p)
	if result == "" || result == "." {
		return "/"
	}
	if result[0] != '/' {
		return "/" + result
	}
	return result
}
