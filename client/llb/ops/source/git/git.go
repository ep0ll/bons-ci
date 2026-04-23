// Package git provides the git repository source operation.
package git

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/gitutil"
	"github.com/moby/buildkit/util/sshutil"
)

// ─── Typed ref constructors ───────────────────────────────────────────────────

// RefKind distinguishes the semantic type of a git reference.
type RefKind string

const (
	RefKindBranch RefKind = "branch"
	RefKindTag    RefKind = "tag"
	RefKindCommit RefKind = "commit"
	RefKindRaw    RefKind = "raw"
)

// Ref is a semantically-typed git reference.
type Ref struct {
	Kind  RefKind
	Value string
}

func BranchRef(name string) Ref { return Ref{Kind: RefKindBranch, Value: name} }
func TagRef(name string) Ref    { return Ref{Kind: RefKindTag, Value: name} }
func CommitRef(sha string) Ref  { return Ref{Kind: RefKindCommit, Value: sha} }
func RawRef(s string) Ref       { return Ref{Kind: RefKindRaw, Value: s} }

func (r Ref) String() string { return r.Value }
func (r Ref) IsZero() bool   { return r.Value == "" }

// ─── MTimePolicy ─────────────────────────────────────────────────────────────

type MTimePolicy string

const (
	MTimePolicyCheckout MTimePolicy = ""
	MTimePolicyCommit   MTimePolicy = "commit"
)

// ─── Config ───────────────────────────────────────────────────────────────────

type Config struct {
	// Remote is the full repository URL. Required.
	Remote string
	// Ref is the git reference to check out.
	Ref Ref
	// SubDirectory exposes only a sub-path of the repository.
	SubDirectory string
	// KeepGitDir preserves the .git directory.
	KeepGitDir bool
	// ShallowClone performs --depth=1.
	ShallowClone bool
	// SkipSubmodules skips recursive submodule init.
	SkipSubmodules bool
	// AuthTokenSecret names the BuildKit secret for HTTP auth tokens.
	AuthTokenSecret string
	// AuthHeaderSecret names the BuildKit secret for raw HTTP auth headers.
	AuthHeaderSecret string
	// KnownSSHHosts overrides accepted SSH host keys.
	KnownSSHHosts string
	// MountSSHSock identifies the SSH agent socket.
	MountSSHSock string
	// Checksum pins the fetched content.
	Checksum string
	// MTimePolicy controls file mtime assignment.
	MTimePolicy MTimePolicy
	Constraints core.Constraints
}

// ─── Option ───────────────────────────────────────────────────────────────────

type Option func(*Config)

func WithRemote(url string) Option         { return func(c *Config) { c.Remote = url } }
func WithRef(ref Ref) Option               { return func(c *Config) { c.Ref = ref } }
func WithSubDirectory(d string) Option     { return func(c *Config) { c.SubDirectory = d } }
func WithKeepGitDir(v bool) Option         { return func(c *Config) { c.KeepGitDir = v } }
func WithShallowClone(v bool) Option       { return func(c *Config) { c.ShallowClone = v } }
func WithSkipSubmodules(v bool) Option     { return func(c *Config) { c.SkipSubmodules = v } }
func WithAuthTokenSecret(s string) Option  { return func(c *Config) { c.AuthTokenSecret = s } }
func WithAuthHeaderSecret(s string) Option { return func(c *Config) { c.AuthHeaderSecret = s } }
func WithKnownSSHHosts(hosts string) Option {
	return func(c *Config) {
		c.KnownSSHHosts = strings.TrimSuffix(hosts, "\n") + "\n"
	}
}
func WithMountSSHSock(id string) Option    { return func(c *Config) { c.MountSSHSock = id } }
func WithChecksum(cs string) Option        { return func(c *Config) { c.Checksum = cs } }
func WithMTimePolicy(p MTimePolicy) Option { return func(c *Config) { c.MTimePolicy = p } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

type Vertex struct {
	config     Config
	cache      marshal.Cache
	identifier string
	attrs      map[string]string
}

func New(opts ...Option) (*Vertex, error) {
	cfg := Config{
		ShallowClone:     true,
		AuthTokenSecret:  "GIT_AUTH_TOKEN",
		AuthHeaderSecret: "GIT_AUTH_HEADER",
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Remote == "" {
		return nil, fmt.Errorf("git.New: Remote is required")
	}
	v := &Vertex{config: cfg}
	if err := v.build(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Vertex) build() error {
	cfg := &v.config
	remote, err := gitutil.ParseURL(cfg.Remote)
	if err != nil && !strings.Contains(err.Error(), "unknown protocol") {
		return fmt.Errorf("git: parse remote: %w", err)
	}

	fullURL := cfg.Remote
	var id string
	if remote != nil {
		fullURL = cfg.Remote
		id = remote.Host + path.Join("/", remote.Path)
	} else {
		fullURL = "https://" + cfg.Remote
		remote2, _ := gitutil.ParseURL(fullURL)
		if remote2 != nil {
			id = remote2.Host + path.Join("/", remote2.Path)
		} else {
			id = cfg.Remote
		}
	}

	ref := cfg.Ref.String()
	subdir := cfg.SubDirectory
	if ref != "" || subdir != "" {
		id += "#" + ref
		if subdir != "" {
			id += ":" + subdir
		}
	}
	v.identifier = "git://" + id

	attrs := make(map[string]string)
	if fullURL != "" {
		attrs[pb.AttrFullRemoteURL] = fullURL
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGitFullURL)
	}
	if cfg.KeepGitDir {
		attrs[pb.AttrKeepGitDir] = "true"
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGitKeepDir)
	}
	if cfg.AuthTokenSecret != "" {
		attrs[pb.AttrAuthTokenSecret] = cfg.AuthTokenSecret
	}
	if cfg.AuthHeaderSecret != "" {
		attrs[pb.AttrAuthHeaderSecret] = cfg.AuthHeaderSecret
	}
	if cfg.SkipSubmodules {
		attrs[pb.AttrGitSkipSubmodules] = "true"
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGitSkipSubmodules)
	}
	if cfg.Checksum != "" {
		attrs[pb.AttrGitChecksum] = cfg.Checksum
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGitChecksum)
	}
	if cfg.MTimePolicy == MTimePolicyCommit {
		attrs[pb.AttrGitMTime] = "commit"
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGitMTime)
	}
	if remote != nil && remote.Scheme == gitutil.SSHProtocol {
		if cfg.KnownSSHHosts != "" {
			attrs[pb.AttrKnownSSHHosts] = cfg.KnownSSHHosts
		} else if scanned, err := sshutil.SSHKeyScan(remote.Host); err == nil {
			attrs[pb.AttrKnownSSHHosts] = scanned
		}
		sock := cfg.MountSSHSock
		if sock == "" {
			sock = "default"
		}
		attrs[pb.AttrMountSSHSock] = sock
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGitKnownSSHHosts)
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGitMountSSHSock)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceGit)
	v.attrs = attrs
	return nil
}

// ─── core.Vertex ──────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSource }
func (v *Vertex) Inputs() []core.Edge   { return nil }
func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "repository root"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Remote == "" {
		return &core.ValidationError{Field: "Remote", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}
	pop, md := marshal.MarshalConstraints(c, &v.config.Constraints)
	pop.Platform = nil
	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: v.identifier,
		Attrs:      v.attrs,
	}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("git.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) != 0 {
		return nil, &core.IncompatibleInputsError{VertexType: v.Type(), Want: "exactly 0", Got: len(inputs)}
	}
	return v, nil
}

func (v *Vertex) Remote() string { return v.config.Remote }
func (v *Vertex) Ref() Ref       { return v.config.Ref }
func (v *Vertex) Config() Config { return v.config }

func (v *Vertex) WithOption(opt Option) (*Vertex, error) {
	cfg := v.config
	opt(&cfg)
	nv := &Vertex{config: cfg}
	if err := nv.build(); err != nil {
		return nil, err
	}
	return nv, nil
}

func (v *Vertex) Output() core.Output { return &gitOutput{v: v} }

type gitOutput struct{ v *Vertex }

func (o *gitOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *gitOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
