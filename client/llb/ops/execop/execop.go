// Package execop implements the ExecOp vertex for LLB graph construction.
// ExecOp executes a command inside a container with configurable mounts,
// secrets, SSH forwarding, network mode, and security settings.
package execop

import (
	"context"
	"sort"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// mount represents a single mount configuration for an exec operation.
type mount struct {
	target       string
	readonly     bool
	source       llb.Output
	output       llb.Output
	selector     string
	cacheID      string
	tmpfs        bool
	tmpfsSize    int64
	cacheSharing CacheMountSharingMode
	noOutput     bool
	contentCache MountContentCache
}

// CacheMountSharingMode defines how a cache mount is shared.
type CacheMountSharingMode int

const (
	CacheMountShared  CacheMountSharingMode = iota // Concurrent access allowed
	CacheMountPrivate                              // Exclusive per-build access
	CacheMountLocked                               // Exclusive with locking
)

// MountContentCache controls cache content semantics.
type MountContentCache int

const (
	MountContentCacheDefault MountContentCache = iota
	MountContentCacheOn
	MountContentCacheOff
)

// SecretInfo describes a secret to mount into the container.
type SecretInfo struct {
	ID       string
	Target   string
	Mode     int
	UID      int
	GID      int
	Optional bool
	IsEnv    bool
}

// SSHInfo describes an SSH agent socket to forward into the container.
type SSHInfo struct {
	ID       string
	Target   string
	Mode     int
	UID      int
	GID      int
	Optional bool
}

// ProxyEnv holds proxy environment variables.
type ProxyEnv struct {
	HTTPProxy  string
	HTTPSProxy string
	FTPProxy   string
	NoProxy    string
	AllProxy   string
}

// ─────────────────────────────────────────────────────────────────────────────
// ExecOp
// ─────────────────────────────────────────────────────────────────────────────

// ExecOp represents a container execution vertex in the LLB graph. It
// executes a program with a root filesystem and optional additional mounts.
type ExecOp struct {
	cache       llb.MarshalCache
	proxyEnv    *ProxyEnv
	root        llb.Output
	mounts      []*mount
	base        llb.State
	constraints llb.Constraints
	isValidated bool
	secrets     []SecretInfo
	ssh         []SSHInfo
}

var _ llb.Vertex = (*ExecOp)(nil)

// NewExecOp creates a new exec operation with the given base state.
func NewExecOp(base llb.State, proxyEnv *ProxyEnv, readOnly bool, c llb.Constraints) *ExecOp {
	e := &ExecOp{
		base:        base,
		proxyEnv:    proxyEnv,
		constraints: c,
	}
	rootOutput := base.Output()
	e.root = rootOutput

	rootMount := &mount{
		target:   pb.RootMount,
		source:   rootOutput,
		readonly: readOnly,
	}
	if !readOnly {
		rootMount.output = llb.NewOutputWithIndex(e, nil, e.getMountIndexFn(rootMount))
	}
	e.mounts = append(e.mounts, rootMount)

	return e
}

// AddMount adds an additional mount to the exec.
func (e *ExecOp) AddMount(target string, source llb.Output, opts ...MountOption) llb.Output {
	m := &mount{target: target, source: source}
	for _, o := range opts {
		o(m)
	}
	e.mounts = append(e.mounts, m)

	if m.readonly || m.tmpfs || m.cacheID != "" {
		m.output = source
		return source
	}

	if m.noOutput {
		return source
	}

	m.output = llb.NewOutputWithIndex(e, nil, e.getMountIndexFn(m))
	return m.output
}

// GetMount returns the output for a mount at the given target.
func (e *ExecOp) GetMount(target string) llb.Output {
	for _, m := range e.mounts {
		if m.target == target {
			return m.output
		}
	}
	return nil
}

// Validate checks the exec operation for correctness.
func (e *ExecOp) Validate(ctx context.Context, c *llb.Constraints) error {
	if e.isValidated {
		return nil
	}
	args, err := e.base.GetArgs(ctx)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("exec requires at least one argument (the command to run)")
	}
	e.isValidated = true
	return nil
}

// Marshal serializes the ExecOp into a pb.Op with a pb.ExecOp payload.
func (e *ExecOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := e.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := e.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	pop, md := llb.MarshalConstraints(constraints, &e.constraints)

	// Build exec meta from state values.
	meta := &pb.Meta{}

	args, _ := e.base.GetArgs(ctx)
	meta.Args = args

	cwd, _ := e.base.GetDir(ctx)
	if cwd != "" {
		meta.Cwd = cwd
	}

	env, _ := e.base.GetEnv(ctx)
	if env != nil {
		meta.Env = env.ToArray()
	}

	user, _ := e.base.GetUser(ctx)
	if user != "" {
		meta.User = user
	}

	hostname, _ := e.base.GetHostname(ctx)
	if hostname != "" {
		meta.Hostname = hostname
	}

	extraHosts, _ := e.base.GetExtraHosts(ctx)
	for _, h := range extraHosts {
		meta.ExtraHosts = append(meta.ExtraHosts, &pb.HostIP{
			Host: h.Host,
			IP:   h.IP.String(),
		})
	}

	network, _ := e.base.GetNetwork(ctx)

	// Build proxy env.
	if e.proxyEnv != nil {
		meta.ProxyEnv = &pb.ProxyEnv{
			HttpProxy:  e.proxyEnv.HTTPProxy,
			HttpsProxy: e.proxyEnv.HTTPSProxy,
			FtpProxy:   e.proxyEnv.FTPProxy,
			NoProxy:    e.proxyEnv.NoProxy,
			AllProxy:   e.proxyEnv.AllProxy,
		}
	}

	peo := &pb.ExecOp{
		Meta:    meta,
		Network: network,
	}

	// Sort mounts for deterministic output.
	sort.Slice(e.mounts, func(i, j int) bool {
		return e.mounts[i].target < e.mounts[j].target
	})

	// Build mount list.
	for _, m := range e.mounts {
		pm := &pb.Mount{
			Dest:     m.target,
			Readonly: m.readonly,
		}

		if m.source != nil {
			pbInput, err := m.source.ToInput(ctx, constraints)
			if err != nil {
				return "", nil, nil, nil, err
			}
			pm.Input = int64(len(pop.Inputs))
			pop.Inputs = append(pop.Inputs, pbInput)
		} else {
			pm.Input = int64(pb.Empty)
		}

		if m.output != nil && !m.readonly && !m.tmpfs {
			pm.Output = int64(0) // will be resolved by solver
		} else {
			pm.Output = int64(pb.SkipOutput)
		}

		if m.selector != "" {
			pm.Selector = m.selector
		}

		if m.tmpfs {
			pm.MountType = pb.MountType_TMPFS
			if m.tmpfsSize > 0 {
				pm.TmpfsOpt = &pb.TmpfsOpt{Size: m.tmpfsSize}
			}
		} else if m.cacheID != "" {
			pm.MountType = pb.MountType_CACHE
			pm.CacheOpt = &pb.CacheOpt{
				ID:      m.cacheID,
				Sharing: pb.CacheSharingOpt(m.cacheSharing),
			}
		} else {
			pm.MountType = pb.MountType_BIND
		}

		peo.Mounts = append(peo.Mounts, pm)
	}

	// Secrets.
	for _, s := range e.secrets {
		if s.IsEnv {
			peo.Secretenv = append(peo.Secretenv, &pb.SecretEnv{
				ID:       s.ID,
				Name:     s.Target,
				Optional: s.Optional,
			})
		} else {
			mountIdx := len(peo.Mounts)
			peo.Mounts = append(peo.Mounts, &pb.Mount{
				Dest:      s.Target,
				MountType: pb.MountType_SECRET,
				SecretOpt: &pb.SecretOpt{
					ID:       s.ID,
					Mode:     uint32(s.Mode),
					Uid:      uint32(s.UID),
					Gid:      uint32(s.GID),
					Optional: s.Optional,
				},
				Input:  int64(pb.Empty),
				Output: int64(pb.SkipOutput),
			})
			_ = mountIdx
		}
	}

	// SSH.
	for _, s := range e.ssh {
		peo.Mounts = append(peo.Mounts, &pb.Mount{
			Dest:      s.Target,
			MountType: pb.MountType_SSH,
			SSHOpt: &pb.SSHOpt{
				ID:       s.ID,
				Mode:     uint32(s.Mode),
				Uid:      uint32(s.UID),
				Gid:      uint32(s.GID),
				Optional: s.Optional,
			},
			Input:  int64(pb.Empty),
			Output: int64(pb.SkipOutput),
		})
	}

	pop.Op = &pb.Op_Exec{Exec: peo}

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, e.constraints.SourceLocations, constraints)
}

// Output returns the root mount output.
func (e *ExecOp) Output() llb.Output {
	return e.mounts[0].output
}

// Inputs returns all mount source outputs.
func (e *ExecOp) Inputs() []llb.Output {
	var inputs []llb.Output
	for _, m := range e.mounts {
		if m.source != nil {
			inputs = append(inputs, m.source)
		}
	}
	return inputs
}

// getMountIndexFn returns a closure that computes the output index for a mount.
func (e *ExecOp) getMountIndexFn(target *mount) func() (pb.OutputIndex, error) {
	return func() (pb.OutputIndex, error) {
		var idx int
		for _, m := range e.mounts {
			if m == target {
				return pb.OutputIndex(idx), nil
			}
			if m.output != nil && !m.readonly && !m.tmpfs {
				idx++
			}
		}
		return 0, errors.New("mount not found in exec op")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ExecState
// ─────────────────────────────────────────────────────────────────────────────

// ExecState wraps a State with access to the ExecOp for additional mount ops.
type ExecState struct {
	llb.State
	exec *ExecOp
}

// AddMount adds a mount and returns the output as a State.
func (es ExecState) AddMount(target string, source llb.State, opts ...MountOption) llb.State {
	return llb.NewState(es.exec.AddMount(target, source.Output(), opts...))
}

// GetMount returns a mount's output as a State.
func (es ExecState) GetMount(target string) llb.State {
	return llb.NewState(es.exec.GetMount(target))
}

// Root returns the root filesystem output.
func (es ExecState) Root() llb.State {
	return llb.NewState(es.exec.Output())
}

// ─────────────────────────────────────────────────────────────────────────────
// Options
// ─────────────────────────────────────────────────────────────────────────────

// MountOption configures a mount.
type MountOption func(*mount)

// Readonly makes the mount read-only.
func Readonly(m *mount) { m.readonly = true }

// SourcePath selects a sub-path from the source.
func SourcePath(src string) MountOption {
	return func(m *mount) { m.selector = src }
}

// ForceNoOutput disables output capture for the mount.
func ForceNoOutput(m *mount) { m.noOutput = true }

// AsPersistentCacheDir marks the mount as a persistent cache.
func AsPersistentCacheDir(id string, sharing CacheMountSharingMode) MountOption {
	return func(m *mount) {
		m.cacheID = id
		m.cacheSharing = sharing
	}
}

// Tmpfs marks the mount as a tmpfs.
func Tmpfs(size int64) MountOption {
	return func(m *mount) {
		m.tmpfs = true
		m.tmpfsSize = size
	}
}

// ContentCacheOpt sets the content cache behaviour.
func ContentCacheOpt(c MountContentCache) MountOption {
	return func(m *mount) { m.contentCache = c }
}

// RunOption configures an exec operation at a higher level.
type RunOption func(*ExecInfo)

// ExecInfo collects all configuration for an exec operation.
type ExecInfo struct {
	llb.State
	Constraints llb.Constraints
	Mounts      []MountInfo
	Secrets     []SecretInfo
	SSH         []SSHInfo
	ReadOnly    bool
	ProxyEnv    *ProxyEnv
}

// MountInfo describes a mount to add during Run.
type MountInfo struct {
	Target  string
	Source  llb.State
	Options []MountOption
}

// AddSecret adds a secret to the exec.
func AddSecret(target string, opts ...SecretOption) RunOption {
	return func(ei *ExecInfo) {
		s := SecretInfo{Target: target, Mode: 0400}
		for _, o := range opts {
			o(&s)
		}
		ei.Secrets = append(ei.Secrets, s)
	}
}

// SecretOption configures a secret.
type SecretOption func(*SecretInfo)

// SecretID sets the secret ID. If empty, the target path is used.
func SecretID(id string) SecretOption {
	return func(s *SecretInfo) { s.ID = id }
}

// SecretAsEnv mounts the secret as an environment variable.
func SecretAsEnv() SecretOption {
	return func(s *SecretInfo) { s.IsEnv = true }
}

// SecretFileMode sets the file mode for the mounted secret.
func SecretFileMode(mode int) SecretOption {
	return func(s *SecretInfo) { s.Mode = mode }
}

// AddSSH adds SSH agent forwarding to the exec.
func AddSSH(opts ...SSHOption) RunOption {
	return func(ei *ExecInfo) {
		s := SSHInfo{Target: "/run/buildkit/ssh_agent.0", Mode: 0600}
		for _, o := range opts {
			o(&s)
		}
		ei.SSH = append(ei.SSH, s)
	}
}

// SSHOption configures SSH forwarding.
type SSHOption func(*SSHInfo)

// SSHID sets the SSH agent ID.
func SSHID(id string) SSHOption {
	return func(s *SSHInfo) { s.ID = id }
}

// SSHSocketTarget sets the socket path.
func SSHSocketTarget(target string) SSHOption {
	return func(s *SSHInfo) { s.Target = target }
}

// WithProxy sets the proxy environment for the exec.
func WithProxy(env ProxyEnv) RunOption {
	return func(ei *ExecInfo) { ei.ProxyEnv = &env }
}

// Shlex splits a command string using shell lexer rules.
func Shlex(str string) RunOption {
	return func(ei *ExecInfo) {
		ei.State = ei.State.With(llb.ArgsOption(str))
	}
}

// Args sets the exec arguments directly.
func Args(a []string) RunOption {
	return func(ei *ExecInfo) {
		ei.State = ei.State.With(llb.ArgsOption(a...))
	}
}

// AddMount adds a mount to the exec.
func AddMount(target string, source llb.State, opts ...MountOption) RunOption {
	return func(ei *ExecInfo) {
		ei.Mounts = append(ei.Mounts, MountInfo{
			Target:  target,
			Source:  source,
			Options: opts,
		})
	}
}

// ReadonlyRootFS makes the root filesystem read-only.
func ReadonlyRootFS() RunOption {
	return func(ei *ExecInfo) { ei.ReadOnly = true }
}

// Run executes the configured command. This is called from State.Run.
func Run(base llb.State, opts ...RunOption) ExecState {
	ei := &ExecInfo{State: base}
	for _, o := range opts {
		o(ei)
	}

	exec := NewExecOp(ei.State, ei.ProxyEnv, ei.ReadOnly, ei.Constraints)
	exec.secrets = ei.Secrets
	exec.ssh = ei.SSH

	for _, m := range ei.Mounts {
		exec.AddMount(m.Target, m.Source.Output(), m.Options...)
	}

	return ExecState{
		State: llb.NewState(exec.Output()),
		exec:  exec,
	}
}
