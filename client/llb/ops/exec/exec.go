// Package exec provides the BuildKit exec (container run) operation.
//
// Example
//
//	alpine, _ := image.New(image.WithRef("alpine:3.20"))
//	op, _ := exec.New(
//	    exec.WithRootMount(alpine.Output(), true),
//	    exec.WithCommand("sh", "-c", "echo hello"),
//	    exec.WithWorkingDir("/"),
//	    exec.WithMount(exec.Mount{
//	        Target: "/cache",
//	        Type:   exec.MountTypeCache,
//	        CacheID: "my-cache",
//	        CacheSharing: exec.CacheSharingShared,
//	    }),
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

// ─── Mount types ─────────────────────────────────────────────────────────────

// MountType identifies how a mount is backed.
type MountType int

const (
	MountTypeBind    MountType = iota // bind from a vertex output
	MountTypeCache                    // persistent cache directory
	MountTypeTmpfs                    // in-memory filesystem
	MountTypeSecret                   // injected secret file
	MountTypeSSH                      // forwarded SSH agent socket
)

// CacheSharingMode controls concurrent access to a cache mount.
type CacheSharingMode int

const (
	CacheSharingShared  CacheSharingMode = iota // concurrent build access
	CacheSharingPrivate                         // one copy per build
	CacheSharingLocked                          // serialised exclusive access
)

// ContentCacheMode controls content-addressed caching for bind mounts.
type ContentCacheMode int

const (
	ContentCacheModeDefault ContentCacheMode = iota
	ContentCacheModeOn
	ContentCacheModeOff
)

// ─── Mount ───────────────────────────────────────────────────────────────────

// Mount describes a filesystem mount inside the exec container.
type Mount struct {
	Target       string           // absolute path inside the container
	Source       core.Output      // backing vertex output (nil for tmpfs/cache)
	ReadOnly     bool
	Selector     string           // sub-path within Source
	Type         MountType
	CacheID      string           // identifies a persistent cache volume
	CacheSharing CacheSharingMode
	TmpfsSize    int64            // bytes; 0 = unlimited
	NoOutput     bool             // write-only; output ignored downstream
	ContentCache ContentCacheMode
}

// ─── SecretMount ─────────────────────────────────────────────────────────────

// SecretMount describes a BuildKit secret available inside the container.
type SecretMount struct {
	SecretID   string  // identifier registered with the daemon
	TargetPath *string // file path inside the container (nil = env-var only)
	EnvVarName *string // environment variable name (nil = file only)
	UID, GID   int
	Mode       int
	Optional   bool
}

// ─── SSHSocket ───────────────────────────────────────────────────────────────

// SSHSocket describes a forwarded SSH agent socket.
type SSHSocket struct {
	SocketID         string // BuildKit SSH agent identifier
	Target           string // socket path inside the container
	UID, GID, Mode   int
	Optional         bool
}

// ─── CDIDevice ───────────────────────────────────────────────────────────────

// CDIDevice describes a Container Device Interface device to expose.
type CDIDevice struct {
	Name     string
	Optional bool
}

// ─── ProxyEnv ────────────────────────────────────────────────────────────────

// ProxyEnv carries HTTP proxy variables injected by the daemon.
type ProxyEnv struct {
	HTTPProxy, HTTPSProxy, FTPProxy, NoProxy, AllProxy string
}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds all parameters for an exec op.
type Config struct {
	Command        []string   // argv[0]…argv[N]; required
	Environment    []string   // KEY=VALUE pairs
	WorkingDir     string     // required; defaults to "/"
	User           string
	Hostname       string
	CgroupParent   string
	Network        pb.NetMode
	Security       pb.SecurityMode
	Mounts         []Mount
	Secrets        []SecretMount
	SSHSockets     []SSHSocket
	CDIDevices     []CDIDevice
	ValidExitCodes []int
	ProxyEnv       *ProxyEnv
	ReadOnlyRoot   bool
	Constraints    core.Constraints
}

// ─── Options ─────────────────────────────────────────────────────────────────

type Option func(*Config)

func WithCommand(args ...string) Option { return func(c *Config) { c.Command = args } }
func WithShlex(cmd string) Option       { return func(c *Config) { c.Command = shlexSplit(cmd) } }
func WithWorkingDir(dir string) Option  { return func(c *Config) { c.WorkingDir = dir } }
func WithUser(u string) Option          { return func(c *Config) { c.User = u } }
func WithHostname(h string) Option      { return func(c *Config) { c.Hostname = h } }
func WithCgroupParent(cp string) Option { return func(c *Config) { c.CgroupParent = cp } }
func WithNetworkMode(m pb.NetMode) Option { return func(c *Config) { c.Network = m } }
func WithSecurityMode(m pb.SecurityMode) Option { return func(c *Config) { c.Security = m } }
func WithReadOnlyRoot() Option          { return func(c *Config) { c.ReadOnlyRoot = true } }
func WithProxyEnv(p ProxyEnv) Option    { return func(c *Config) { c.ProxyEnv = &p } }

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

func WithMount(m Mount) Option          { return func(c *Config) { c.Mounts = append(c.Mounts, m) } }
func WithSecret(s SecretMount) Option   { return func(c *Config) { c.Secrets = append(c.Secrets, s) } }
func WithSSHSocket(s SSHSocket) Option  { return func(c *Config) { c.SSHSockets = append(c.SSHSockets, s) } }
func WithCDIDevice(d CDIDevice) Option  { return func(c *Config) { c.CDIDevices = append(c.CDIDevices, d) } }
func WithValidExitCodes(codes ...int) Option { return func(c *Config) { c.ValidExitCodes = codes } }

// WithRootMount sets the root ("/") mount. Replaces any existing root mount.
func WithRootMount(source core.Output, readOnly bool) Option {
	return func(c *Config) {
		root := Mount{Target: pb.RootMount, Source: source, ReadOnly: readOnly}
		for i, m := range c.Mounts {
			if m.Target == pb.RootMount {
				c.Mounts[i] = root
				return
			}
		}
		c.Mounts = append([]Mount{root}, c.Mounts...)
	}
}

func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex is the llbx exec op implementation.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs an exec vertex.
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
		cfg.WorkingDir = "/"
	}
	return &Vertex{config: cfg}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeExec }

func (v *Vertex) Inputs() []core.Edge {
	sorted := v.sortedMounts()
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
		out = append(out, core.Edge{Vertex: m.Source.Vertex(context.Background(), nil), Index: 0})
	}
	return out
}

func (v *Vertex) Outputs() []core.OutputSlot {
	sorted := v.sortedMounts()
	var slots []core.OutputSlot
	idx := 0
	for _, m := range sorted {
		if !m.ReadOnly && m.Type == MountTypeBind && m.CacheID == "" && !m.NoOutput {
			slots = append(slots, core.OutputSlot{Index: idx, Description: m.Target})
			idx++
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
	h := v.cache.Acquire()
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
	if len(cfg.CDIDevices) > 0 {
		cd := make([]*pb.CDIDevice, len(cfg.CDIDevices))
		for i, d := range cfg.CDIDevices {
			cd[i] = &pb.CDIDevice{Name: d.Name, Optional: d.Optional}
		}
		peo.CdiDevices = cd
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMetaCDI)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMetaBase)

	// Build mounts.
	sorted := v.sortedMounts()
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
					return nil, fmt.Errorf("exec.Marshal: mount %q: %w", m.Target, err)
				}
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
		if m.Type == MountTypeCache && m.CacheID != "" {
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
		switch m.ContentCache {
		case ContentCacheModeOn:
			pm.ContentCache = pb.MountContentCache_ON
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountContentCache)
		case ContentCacheModeOff:
			pm.ContentCache = pb.MountContentCache_OFF
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountContentCache)
		}
		peo.Mounts = append(peo.Mounts, pm)
	}

	// Secrets.
	if len(cfg.Secrets) > 0 {
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecMountSecret)
		for _, s := range cfg.Secrets {
			if s.EnvVarName != nil {
				peo.Secretenv = append(peo.Secretenv, &pb.SecretEnv{
					ID: s.SecretID, Name: *s.EnvVarName, Optional: s.Optional,
				})
				core.ConstraintsAddCap(&cfg.Constraints, pb.CapExecSecretEnv)
			}
			if s.TargetPath != nil {
				peo.Mounts = append(peo.Mounts, &pb.Mount{
					Input: int64(pb.Empty), Dest: *s.TargetPath,
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
				Input: int64(pb.Empty), Dest: target,
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
		return nil, fmt.Errorf("exec.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	sorted := v.sortedMounts()
	newMounts := slices.Clone(v.config.Mounts)
	bindIdx := 0
	for _, m := range sorted {
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
		for j := range newMounts {
			if newMounts[j].Target == m.Target {
				newMounts[j].Source = &edgeOutput{edge: inputs[bindIdx]}
				break
			}
		}
		bindIdx++
	}
	newCfg := v.config
	newCfg.Mounts = newMounts
	return &Vertex{config: newCfg}, nil
}

// ─── Output accessors ────────────────────────────────────────────────────────

// OutputFor returns the core.Output for a specific mount target path.
func (v *Vertex) OutputFor(mountTarget string) core.Output {
	sorted := v.sortedMounts()
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
		for _, m := range v.config.Mounts {
			if m.Target == pb.RootMount {
				return m.Source
			}
		}
		return nil
	}
	return v.OutputFor(pb.RootMount)
}

func (v *Vertex) Config() Config { return v.config }

func (v *Vertex) WithOption(opt Option) (*Vertex, error) {
	newCfg := v.config
	opt(&newCfg)
	if len(newCfg.Command) == 0 {
		return nil, fmt.Errorf("exec.WithOption: Command is required")
	}
	return &Vertex{config: newCfg}, nil
}

// ─── internal helpers ─────────────────────────────────────────────────────────

func (v *Vertex) sortedMounts() []Mount {
	sorted := slices.Clone(v.config.Mounts)
	slices.SortFunc(sorted, func(a, b Mount) int {
		return strings.Compare(a.Target, b.Target)
	})
	return sorted
}

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

type edgeOutput struct{ edge core.Edge }

func (e *edgeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return e.edge.Vertex }
func (e *edgeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := e.edge.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.edge.Index)}, nil
}

func shlexSplit(s string) []string {
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

var _ = digest.Digest("")

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
