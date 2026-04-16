// Package exec provides the exec (container run) operation for llbx.
//
// Example
//
//	rootfs := image.Must(image.WithRef("alpine:3.20")).Output()
//
//	op := exec.New(
//	    exec.WithCommand("sh", "-c", "echo hello > /out/hello"),
//	    exec.WithWorkingDir("/"),
//	    exec.WithMount(exec.Mount{
//	        Target: "/out",
//	        Source: scratch.Output(),
//	        ReadOnly: false,
//	    }),
//	    exec.WithRootMount(rootfs, false),
//	)
package exec

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// ─── Mount ────────────────────────────────────────────────────────────────────

// MountType identifies how a mount is backed.
type MountType int

const (
	MountTypeBind   MountType = iota // standard bind mount from a vertex output
	MountTypeCache                   // persistent cache directory
	MountTypeTmpfs                   // in-memory tmpfs
	MountTypeSecret                  // secret file
	MountTypeSSH                     // SSH agent socket
)

// CacheSharingMode controls how a cache mount is shared across concurrent builds.
type CacheSharingMode int

const (
	CacheSharingShared  CacheSharingMode = iota // multiple builds share simultaneously
	CacheSharingPrivate                         // each build gets its own copy
	CacheSharingLocked                          // serialised access (exclusive)
)

// Mount describes a filesystem mount within the exec container.
type Mount struct {
	// Target is the absolute path inside the container.
	Target string

	// Source is the vertex output providing the mount's contents.
	// Nil for tmpfs and cache mounts.
	Source core.Output

	// ReadOnly prevents writes to the mount.
	ReadOnly bool

	// Selector is a sub-path within Source to expose at Target.
	Selector string

	// Type selects the mount backing.
	Type MountType

	// CacheID identifies a persistent cache volume (Type == MountTypeCache).
	CacheID string
	// CacheSharing controls concurrent access to the cache volume.
	CacheSharing CacheSharingMode

	// TmpfsSize limits the tmpfs size in bytes (0 = unlimited).
	TmpfsSize int64

	// NoOutput marks the mount as write-only (output ignored by downstream).
	NoOutput bool

	// ContentCache controls the content-addressable cache for bind mounts.
	ContentCache ContentCacheMode
}

// ContentCacheMode controls content-addressed caching for bind mounts.
type ContentCacheMode int

const (
	ContentCacheModeDefault ContentCacheMode = iota
	ContentCacheModeOn
	ContentCacheModeOff
)

// ─── Secret / SSH ─────────────────────────────────────────────────────────────

// SecretMount describes a BuildKit secret available inside the container.
type SecretMount struct {
	// SecretID is the secret identifier registered with the daemon.
	SecretID string
	// TargetPath is the file path inside the container (nil = env var only).
	TargetPath *string
	// EnvVarName is the environment variable name (nil = file mount only).
	EnvVarName *string
	// UID/GID/Mode control the mounted secret file's ownership and permissions.
	UID, GID int
	Mode     int
	Optional bool
}

// SSHSocket describes an SSH agent socket forwarded into the container.
type SSHSocket struct {
	// SocketID identifies the SSH agent registered with the daemon.
	SocketID string
	// Target is the socket path inside the container.
	// Defaults to "/run/buildkit/ssh_agent.<N>".
	Target         string
	UID, GID, Mode int
	Optional       bool
}

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all parameters for an exec (run) op.
type Config struct {
	// Command is the argument vector (argv[0] … argv[N]).
	Command []string

	// Environment variables in "KEY=VALUE" format.
	// Use the Env builder methods rather than populating directly.
	Environment []string

	// WorkingDir is the process working directory inside the container.
	WorkingDir string

	// User is the username or UID:GID that the process runs as.
	User string

	// Hostname sets the container hostname (requires CapExecMetaHostname).
	Hostname string

	// CgroupParent sets the parent cgroup path.
	CgroupParent string

	// Network mode (unset = sandbox).
	Network pb.NetMode

	// Security mode (sandbox by default).
	Security pb.SecurityMode

	// Mounts are the ordered list of filesystem mounts.
	// The root mount (at "/") is always present; additional mounts are appended.
	Mounts []Mount

	// Secrets are secret mounts injected into the container.
	Secrets []SecretMount

	// SSHSockets are SSH agent sockets forwarded into the container.
	SSHSockets []SSHSocket

	// ValidExitCodes is the set of exit codes considered successful (nil = {0}).
	ValidExitCodes []int

	// ProxyEnv carries HTTP proxy environment variables injected by the daemon.
	ProxyEnv *ProxyEnv

	// ReadOnlyRoot makes the root filesystem read-only.
	ReadOnlyRoot bool

	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// ProxyEnv carries proxy variables injected by the BuildKit daemon.
type ProxyEnv struct {
	HTTPProxy, HTTPSProxy, FTPProxy, NoProxy, AllProxy string
}

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// WithCommand sets the command and arguments.
func WithCommand(args ...string) Option {
	return func(c *Config) { c.Command = args }
}

// WithShell parses a shell command string using shlex.
func WithShlex(cmd string) Option {
	return func(c *Config) {
		// Simple shlex split: split on spaces, honour single/double quotes.
		c.Command = shlexSplit(cmd)
	}
}

// WithEnv appends an environment variable.
func WithEnv(key, value string) Option {
	return func(c *Config) {
		for i, e := range c.Environment {
			if strings.HasPrefix(e, key+"=") {
				c.Environment[i] = key + "=" + value
				return
			}
		}
		c.Environment = append(c.Environment, key+"="+value)
	}
}

// WithWorkingDir sets the working directory.
func WithWorkingDir(dir string) Option {
	return func(c *Config) { c.WorkingDir = dir }
}

// WithUser sets the container user.
func WithUser(user string) Option { return func(c *Config) { c.User = user } }

// WithHostname sets the container hostname.
func WithHostname(h string) Option { return func(c *Config) { c.Hostname = h } }

// WithCgroupParent sets the cgroup parent path.
func WithCgroupParent(cp string) Option { return func(c *Config) { c.CgroupParent = cp } }

// WithNetworkMode sets the network isolation mode.
func WithNetworkMode(mode pb.NetMode) Option { return func(c *Config) { c.Network = mode } }

// WithSecurityMode sets the security mode.
func WithSecurityMode(mode pb.SecurityMode) Option { return func(c *Config) { c.Security = mode } }

// WithReadOnlyRoot makes the root filesystem read-only.
func WithReadOnlyRoot() Option { return func(c *Config) { c.ReadOnlyRoot = true } }

// WithMount appends a filesystem mount.
func WithMount(m Mount) Option {
	return func(c *Config) { c.Mounts = append(c.Mounts, m) }
}

// WithRootMount sets the root ("/") filesystem mount.
func WithRootMount(source core.Output, readOnly bool) Option {
	return func(c *Config) {
		root := Mount{
			Target:   pb.RootMount,
			Source:   source,
			ReadOnly: readOnly,
		}
		// Replace existing root mount if present.
		for i, m := range c.Mounts {
			if m.Target == pb.RootMount {
				c.Mounts[i] = root
				return
			}
		}
		c.Mounts = append([]Mount{root}, c.Mounts...)
	}
}

// WithSecret appends a secret mount.
func WithSecret(s SecretMount) Option {
	return func(c *Config) { c.Secrets = append(c.Secrets, s) }
}

// WithSSHSocket appends an SSH agent socket forward.
func WithSSHSocket(s SSHSocket) Option {
	return func(c *Config) { c.SSHSockets = append(c.SSHSockets, s) }
}

// WithValidExitCodes sets the exit codes considered successful.
func WithValidExitCodes(codes ...int) Option {
	return func(c *Config) { c.ValidExitCodes = codes }
}

// WithProxyEnv sets the HTTP proxy environment.
func WithProxyEnv(p ProxyEnv) Option { return func(c *Config) { c.ProxyEnv = &p } }

// WithConstraintsOption applies a core.ConstraintsOption.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx implementation of the exec op.
// It is immutable; use New() or WithOption() to construct variants.
type Vertex struct {
	config Config
	cache  marshal.Cache

	// inputs mirrors config.Mounts in sorted order for stable digests.
	// Populated lazily in Marshal.
	sortedMounts []Mount
}

// New constructs an exec Vertex from options.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{
		WorkingDir: "/",
		Network:    pb.NetMode_UNSET,
		Security:   pb.SecurityMode_SANDBOX,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("exec.New: Command is required")
	}
	if cfg.WorkingDir == "" {
		return nil, fmt.Errorf("exec.New: WorkingDir must not be empty")
	}
	v := &Vertex{config: cfg}
	return v, nil
}

// ─── core.Vertex ──────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeExec }

// Inputs returns the deduplicated source outputs of all bind mounts,
// in the same sorted order as Marshal uses.
func (v *Vertex) Inputs() []core.Edge {
	sorted := v.sortMounts()
	seen := map[core.Output]struct{}{}
	var out []core.Edge
	for _, m := range sorted {
		if m.Source == nil || m.Type == MountTypeCache || m.Type == MountTypeTmpfs {
			continue
		}
		if _, ok := seen[m.Source]; ok {
			continue
		}
		seen[m.Source] = struct{}{}
		out = append(out, core.Edge{Vertex: nil /* placeholder */, Index: 0})
		// Note: edge.Vertex is resolved dynamically in Marshal via m.Source.Vertex().
		// The inputs list here is used by graph.walk for traversal.
		_ = m.Source // suppress lint
	}
	return out
}

func (v *Vertex) Outputs() []core.OutputSlot {
	sorted := v.sortMounts()
	var slots []core.OutputSlot
	outIdx := 0
	for _, m := range sorted {
		if !m.ReadOnly && m.Type == MountTypeBind && m.CacheID == "" && !m.NoOutput {
			slots = append(slots, core.OutputSlot{
				Index:       outIdx,
				Description: m.Target,
			})
			outIdx++
		}
	}
	return slots
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if len(v.config.Command) == 0 {
		return &core.ValidationError{Field: "Command", Cause: fmt.Errorf("must not be empty")}
	}
	if v.config.WorkingDir == "" {
		return &core.ValidationError{Field: "WorkingDir", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := marshal.Acquire(&v.cache)
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	if err := v.Validate(ctx, c); err != nil {
		return nil, err
	}

	cfg := &v.config
	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)

	peo := &pb.ExecOp{
		Meta: &pb.Meta{
			Args:                      cfg.Command,
			Env:                       cfg.Environment,
			Cwd:                       cfg.WorkingDir,
			User:                      cfg.User,
			Hostname:                  cfg.Hostname,
			CgroupParent:              cfg.CgroupParent,
			RemoveMountStubsRecursive: true,
		},
		Network:  cfg.Network,
		Security: cfg.Security,
	}

	if cfg.Network != pb.NetMode_UNSET {
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMetaNetwork)
	}
	if cfg.Security != pb.SecurityMode_SANDBOX {
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMetaSecurity)
	}
	if cfg.ProxyEnv != nil {
		peo.Meta.ProxyEnv = &pb.ProxyEnv{
			HttpProxy:  cfg.ProxyEnv.HTTPProxy,
			HttpsProxy: cfg.ProxyEnv.HTTPSProxy,
			FtpProxy:   cfg.ProxyEnv.FTPProxy,
			NoProxy:    cfg.ProxyEnv.NoProxy,
			AllProxy:   cfg.ProxyEnv.AllProxy,
		}
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMetaProxy)
	}
	if len(cfg.ValidExitCodes) > 0 {
		codes := make([]int32, len(cfg.ValidExitCodes))
		for i, code := range cfg.ValidExitCodes {
			codes[i] = int32(code)
		}
		peo.Meta.ValidExitCodes = codes
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecValidExitCode)
	}

	core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMetaBase)

	// Collect and sort mounts.
	sorted := v.sortMounts()

	outIndex := 0
	for _, m := range sorted {
		var inputIndex pb.InputIndex

		switch m.Type {
		case MountTypeTmpfs:
			inputIndex = pb.Empty
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountTmpfs)
		case MountTypeCache:
			inputIndex = pb.Empty
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountCache)
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountCacheSharing)
		default:
			if m.Source == nil {
				inputIndex = pb.Empty
			} else {
				inp, err := m.Source.ToInput(ctx, c)
				if err != nil {
					return nil, fmt.Errorf("exec.Vertex.Marshal mount %q: %w", m.Target, err)
				}
				// Dedup inputs.
				found := false
				for i, existing := range pop.Inputs {
					if existing.Digest == inp.Digest && existing.Index == inp.Index {
						inputIndex = pb.InputIndex(i)
						found = true
						break
					}
				}
				if !found {
					inputIndex = pb.InputIndex(len(pop.Inputs))
					pop.Inputs = append(pop.Inputs, inp)
				}
				core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountBind)
			}
		}

		outputIndex := pb.SkipOutput
		if !m.NoOutput && !m.ReadOnly && m.Type == MountTypeBind && m.CacheID == "" {
			outputIndex = pb.OutputIndex(outIndex)
			outIndex++
		}

		if m.Selector != "" {
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountSelector)
		}

		pm := &pb.Mount{
			Input:    int64(inputIndex),
			Dest:     m.Target,
			Readonly: m.ReadOnly,
			Output:   int64(outputIndex),
			Selector: m.Selector,
		}
		if m.Type == MountTypeCache {
			pm.MountType = pb.MountType_CACHE
			pm.CacheOpt = &pb.CacheOpt{ID: m.CacheID}
			switch m.CacheSharing {
			case CacheSharingPrivate:
				pm.CacheOpt.Sharing = pb.CacheSharingOpt_PRIVATE
			case CacheSharingLocked:
				pm.CacheOpt.Sharing = pb.CacheSharingOpt_LOCKED
			default:
				pm.CacheOpt.Sharing = pb.CacheSharingOpt_SHARED
			}
		}
		if m.Type == MountTypeTmpfs {
			pm.MountType = pb.MountType_TMPFS
			pm.TmpfsOpt = &pb.TmpfsOpt{Size: m.TmpfsSize}
		}
		if m.ContentCache != ContentCacheModeDefault {
			switch m.ContentCache {
			case ContentCacheModeOn:
				pm.ContentCache = pb.MountContentCache_ON
			case ContentCacheModeOff:
				pm.ContentCache = pb.MountContentCache_OFF
			}
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountContentCache)
		}
		peo.Mounts = append(peo.Mounts, pm)
	}

	// Secret mounts.
	if len(cfg.Secrets) > 0 {
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountSecret)
		for _, s := range cfg.Secrets {
			if s.EnvVarName != nil {
				peo.Secretenv = append(peo.Secretenv, &pb.SecretEnv{
					ID:       s.SecretID,
					Name:     *s.EnvVarName,
					Optional: s.Optional,
				})
				core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecSecretEnv)
			}
			if s.TargetPath != nil {
				peo.Mounts = append(peo.Mounts, &pb.Mount{
					Input:     int64(pb.Empty),
					Dest:      *s.TargetPath,
					MountType: pb.MountType_SECRET,
					SecretOpt: &pb.SecretOpt{
						ID:       s.SecretID,
						Uid:      uint32(s.UID),
						Gid:      uint32(s.GID),
						Mode:     uint32(s.Mode),
						Optional: s.Optional,
					},
				})
			}
		}
	}

	// SSH sockets.
	if len(cfg.SSHSockets) > 0 {
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountSSH)
		for i, s := range cfg.SSHSockets {
			target := s.Target
			if target == "" {
				target = fmt.Sprintf("/run/buildkit/ssh_agent.%d", i)
			}
			peo.Mounts = append(peo.Mounts, &pb.Mount{
				Input:     int64(pb.Empty),
				Dest:      target,
				MountType: pb.MountType_SSH,
				SSHOpt: &pb.SSHOpt{
					ID:       s.SocketID,
					Uid:      uint32(s.UID),
					Gid:      uint32(s.GID),
					Mode:     uint32(s.Mode),
					Optional: s.Optional,
				},
			})
		}
	}

	pop.Op = &pb.Op_Exec{Exec: peo}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("exec.Vertex.Marshal: %w", err)
	}

	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

// ─── MutatingVertex ───────────────────────────────────────────────────────────

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	// For exec ops, inputs correspond to the source outputs of bind mounts.
	// We rebuild the mount list with the new inputs.
	sorted := v.sortMounts()
	bindIdx := 0
	newMounts := make([]Mount, len(v.config.Mounts))
	copy(newMounts, v.config.Mounts)

	for i, m := range sorted {
		if m.Source == nil || m.Type != MountTypeBind {
			continue
		}
		if bindIdx >= len(inputs) {
			return nil, &core.IncompatibleInputsError{
				VertexType: v.Type(),
				Got:        len(inputs),
				Want:       fmt.Sprintf("at least %d", bindIdx+1),
			}
		}
		// Find the corresponding mount in the unsorted list and update its Source.
		for j := range newMounts {
			if newMounts[j].Target == sorted[i].Target {
				newMounts[j].Source = &edgeOutput{edge: inputs[bindIdx]}
				break
			}
		}
		bindIdx++
	}

	newCfg := v.config
	newCfg.Mounts = newMounts
	nv := &Vertex{config: newCfg}
	return nv, nil
}

// ─── Output accessors ─────────────────────────────────────────────────────────

// OutputFor returns the core.Output for a specific mount target path.
// Returns nil if the mount is not found or is read-only/tmpfs/cache.
func (v *Vertex) OutputFor(mountTarget string) core.Output {
	sorted := v.sortMounts()
	outIdx := 0
	for _, m := range sorted {
		if !m.NoOutput && !m.ReadOnly && m.Type == MountTypeBind && m.CacheID == "" {
			if m.Target == mountTarget {
				return &mountOutput{vertex: v, index: outIdx}
			}
			outIdx++
		}
	}
	return nil
}

// RootOutput returns the core.Output for the root ("/") mount.
func (v *Vertex) RootOutput() core.Output {
	if v.config.ReadOnlyRoot {
		// root is read-only; look up its source
		for _, m := range v.config.Mounts {
			if m.Target == pb.RootMount {
				return m.Source
			}
		}
		return nil
	}
	return v.OutputFor(pb.RootMount)
}

// Config returns a copy of the vertex's configuration.
func (v *Vertex) Config() Config { return v.config }

// WithOption returns a new Vertex with the option applied. Cache is invalidated.
func (v *Vertex) WithOption(opt Option) (*Vertex, error) {
	newCfg := v.config
	opt(&newCfg)
	if len(newCfg.Command) == 0 {
		return nil, fmt.Errorf("exec.Vertex.WithOption: Command is required")
	}
	return &Vertex{config: newCfg}, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (v *Vertex) sortMounts() []Mount {
	sorted := slices.Clone(v.config.Mounts)
	slices.SortFunc(sorted, func(a, b Mount) int {
		return strings.Compare(a.Target, b.Target)
	})
	return sorted
}

// mountOutput is a core.Output referencing a specific exec output slot.
type mountOutput struct {
	vertex *Vertex
	index  int
}

func (o *mountOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.vertex }
func (o *mountOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(o.index)}, nil
}

// edgeOutput wraps a core.Edge as a core.Output, used during reparenting.
type edgeOutput struct{ edge core.Edge }

func (e *edgeOutput) Vertex(ctx context.Context, c *core.Constraints) core.Vertex {
	return e.edge.Vertex
}
func (e *edgeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := e.edge.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.edge.Index)}, nil
}

// shlexSplit is a minimal shell-word splitter.
func shlexSplit(s string) []string {
	// Minimal shlex: space-separated, respects double and single quotes.
	var args []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(ch)
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == ' ' || ch == '\t':
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// Compile-time checks.
var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)

// Unused import guard.
var _ = digest.Digest("")
