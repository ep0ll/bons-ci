// Package git provides the Git source operation for llbx.
//
// The Git op fetches a remote repository and exposes its contents as a
// filesystem state. All configuration is explicit and named – there are no
// positional or ad-hoc string parameters.
//
// Example
//
//	src := git.New(
//	    git.WithRemote("https://github.com/moby/buildkit.git"),
//	    git.WithRef(git.TagRef("v0.15.0")),
//	    git.WithKeepGitDir(false),
//	    git.WithAuthTokenSecret("MY_GIT_TOKEN"),
//	)
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

// ─── Ref types ────────────────────────────────────────────────────────────────

// Ref is a resolved git reference (branch, tag, or commit SHA).
// Using a named type prevents accidental mixing of branches and SHAs.
type Ref struct {
	kind  refKind
	value string
}

type refKind string

const (
	refKindBranch refKind = "branch"
	refKindTag    refKind = "tag"
	refKindCommit refKind = "commit"
	refKindRaw    refKind = "raw" // any string – used for backward compat
)

// BranchRef creates a Ref pointing to the tip of the named branch.
func BranchRef(name string) Ref { return Ref{kind: refKindBranch, value: name} }

// TagRef creates a Ref pointing to the named tag.
func TagRef(name string) Ref { return Ref{kind: refKindTag, value: name} }

// CommitRef creates a Ref pointing to a specific commit SHA.
func CommitRef(sha string) Ref { return Ref{kind: refKindCommit, value: sha} }

// RawRef creates a Ref from an arbitrary string (e.g., from user input or
// existing Dockerfiles). Prefer the typed constructors where possible.
func RawRef(s string) Ref { return Ref{kind: refKindRaw, value: s} }

// String returns the raw ref string as expected by the BuildKit source op.
func (r Ref) String() string { return r.value }

// IsZero reports whether the Ref is unset.
func (r Ref) IsZero() bool { return r.value == "" }

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all parameters for the Git source op.
// All fields have safe zero values; use functional options to populate them.
type Config struct {
	// Remote is the full HTTPS/SSH URL of the repository.
	// Required.
	Remote string

	// Ref is the git reference to check out. If zero, the upstream default
	// branch is used (BuildKit behaviour).
	Ref Ref

	// SubDirectory is an optional path within the repository to expose.
	// Equivalent to the ":subdir" fragment in Docker Git URLs.
	SubDirectory string

	// KeepGitDir preserves the ".git" directory in the fetched tree.
	// Default: false (stripped for reproducibility).
	KeepGitDir bool

	// ShallowClone performs a "--depth 1" clone.
	// Default: true.
	ShallowClone bool

	// SkipSubmodules skips recursive submodule initialisation.
	// Default: false.
	SkipSubmodules bool

	// AuthTokenSecret is the name of a BuildKit secret holding a Git HTTP
	// password/personal-access-token.
	AuthTokenSecret string

	// AuthHeaderSecret is the name of a BuildKit secret holding a raw
	// "Authorization: <value>" header.
	AuthHeaderSecret string

	// KnownSSHHosts overrides the list of accepted SSH host keys.
	// If empty and the remote uses SSH, llbx will attempt ssh-keyscan.
	KnownSSHHosts string

	// MountSSHSock is the SSH agent socket identifier to mount.
	// "default" uses the daemon's default agent.
	MountSSHSock string

	// Checksum is the expected SHA256 of the fetched content.
	// Provides immutability guarantees similar to `git.commit`.
	Checksum string

	// MTimePolicy controls how file modification times are set.
	// "commit" sets mtimes to the resolved commit timestamp.
	// "checkout" (default) uses the checkout time.
	MTimePolicy MTimePolicy

	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// MTimePolicy controls how modification times are assigned to fetched files.
type MTimePolicy string

const (
	// MTimePolicyCheckout uses the checkout time (default, BuildKit behaviour).
	MTimePolicyCheckout MTimePolicy = ""
	// MTimePolicyCommit sets each file's mtime to the resolved commit timestamp.
	MTimePolicyCommit MTimePolicy = "commit"
)

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// WithRemote sets the repository URL.
func WithRemote(url string) Option {
	return func(c *Config) { c.Remote = url }
}

// WithRef sets the git reference.
func WithRef(ref Ref) Option {
	return func(c *Config) { c.Ref = ref }
}

// WithSubDirectory sets the sub-directory within the repo to expose.
func WithSubDirectory(subdir string) Option {
	return func(c *Config) { c.SubDirectory = subdir }
}

// WithKeepGitDir controls whether ".git" is preserved.
func WithKeepGitDir(keep bool) Option {
	return func(c *Config) { c.KeepGitDir = keep }
}

// WithShallowClone controls whether to perform a shallow clone.
func WithShallowClone(shallow bool) Option {
	return func(c *Config) { c.ShallowClone = shallow }
}

// WithSkipSubmodules controls submodule initialisation.
func WithSkipSubmodules(skip bool) Option {
	return func(c *Config) { c.SkipSubmodules = skip }
}

// WithAuthTokenSecret sets the secret name for HTTP auth tokens.
func WithAuthTokenSecret(secretName string) Option {
	return func(c *Config) { c.AuthTokenSecret = secretName }
}

// WithAuthHeaderSecret sets the secret name for raw auth headers.
func WithAuthHeaderSecret(secretName string) Option {
	return func(c *Config) { c.AuthHeaderSecret = secretName }
}

// WithKnownSSHHosts sets the known_hosts entries for SSH remotes.
func WithKnownSSHHosts(hosts string) Option {
	return func(c *Config) {
		c.KnownSSHHosts = strings.TrimSuffix(hosts, "\n") + "\n"
	}
}

// WithMountSSHSock specifies the SSH agent socket identifier.
func WithMountSSHSock(sockID string) Option {
	return func(c *Config) { c.MountSSHSock = sockID }
}

// WithChecksum pins the content to an expected SHA256 checksum.
func WithChecksum(checksum string) Option {
	return func(c *Config) { c.Checksum = checksum }
}

// WithMTimePolicy sets the file mtime assignment strategy.
func WithMTimePolicy(p MTimePolicy) Option {
	return func(c *Config) { c.MTimePolicy = p }
}

// WithConstraintsOption applies a core.ConstraintsOption to the embedded
// Constraints, enabling source maps, capability sets, etc.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx core.Vertex implementation for the Git source op.
// It is immutable after construction; use New to build one.
type Vertex struct {
	config Config
	cache  marshal.Cache

	// identifier is the canonical "git://<host><path>#<ref>:<subdir>" string.
	identifier string
	// attrs are the BuildKit source op attributes, built once from config.
	attrs map[string]string
}

// New constructs a Git source Vertex from the supplied options.
// Returns an error if required fields (Remote) are missing.
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

// build pre-computes the identifier and attribute map from the config.
// This keeps Marshal fast by doing the work once.
func (v *Vertex) build() error {
	cfg := &v.config
	remote, err := gitutil.ParseURL(cfg.Remote)
	if err != nil && !isUnknownProtocol(err) {
		return fmt.Errorf("git.Vertex.build: parse remote URL: %w", err)
	}
	var fullURL string
	if remote != nil {
		fullURL = cfg.Remote
		id := remote.Host + path.Join("/", remote.Path)
		ref := cfg.Ref.String()
		subdir := cfg.SubDirectory
		if ref != "" || subdir != "" {
			id += "#" + ref
			if subdir != "" {
				id += ":" + subdir
			}
		}
		v.identifier = "git://" + id
	} else {
		v.identifier = "git://" + cfg.Remote
		fullURL = "https://" + cfg.Remote
	}

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

	// SSH-specific setup.
	if remote != nil && remote.Scheme == gitutil.SSHProtocol {
		if cfg.KnownSSHHosts != "" {
			attrs[pb.AttrKnownSSHHosts] = cfg.KnownSSHHosts
		} else {
			if scanned, err := sshutil.SSHKeyScan(remote.Host); err == nil {
				attrs[pb.AttrKnownSSHHosts] = scanned
			}
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

// ─── core.Vertex implementation ───────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSource }

func (v *Vertex) Inputs() []core.Edge { return nil } // source ops have no inputs

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "repository root"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Remote == "" {
		return &core.ValidationError{Field: "Remote", Cause: fmt.Errorf("must not be empty")}
	}
	if v.config.Ref.IsZero() && v.config.SubDirectory == "" {
		// Valid – defaults to upstream HEAD.
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

	pop, md := marshal.MarshalConstraints(c, &v.config.Constraints)
	pop.Platform = nil // git source is not platform-specific
	pop.Op = &pb.Op_Source{
		Source: &pb.SourceOp{
			Identifier: v.identifier,
			Attrs:      v.attrs,
		},
	}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("git.Vertex.Marshal: %w", err)
	}

	dgst, bytes, meta, srcs, err := h.Store(bytes, md, c.SourceLocations, c)
	if err != nil {
		return nil, err
	}
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

// ─── MutatingVertex implementation ───────────────────────────────────────────

// WithInputs returns an error because Git source ops have no inputs.
func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) != 0 {
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(),
			Got:        len(inputs),
			Want:       "exactly 0",
			Detail:     "git source ops have no inputs",
		}
	}
	return v, nil
}

// ─── Accessor methods (read-only view of Config) ──────────────────────────────

// Remote returns the configured repository URL.
func (v *Vertex) Remote() string { return v.config.Remote }

// Ref returns the configured git reference.
func (v *Vertex) Ref() Ref { return v.config.Ref }

// SubDirectory returns the configured sub-directory path.
func (v *Vertex) SubDirectory() string { return v.config.SubDirectory }

// Config returns a copy of the vertex's configuration.
func (v *Vertex) Config() Config { return v.config }

// ─── Mutation builders ────────────────────────────────────────────────────────

// WithOption returns a new Vertex identical to the receiver but with the
// given option applied. The receiver is not modified. The cache is invalidated
// because any field change may change the digest.
func (v *Vertex) WithOption(opt Option) (*Vertex, error) {
	newCfg := v.config
	opt(&newCfg)
	nv := &Vertex{config: newCfg}
	if err := nv.build(); err != nil {
		return nil, fmt.Errorf("git.Vertex.WithOption: rebuild: %w", err)
	}
	return nv, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func isUnknownProtocol(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown protocol")
}

// Output returns a core.Output that references output slot 0 of this vertex.
func (v *Vertex) Output() core.Output {
	return &vertexOutput{vertex: v, index: 0}
}

type vertexOutput struct {
	vertex *Vertex
	index  int
}

func (o *vertexOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex {
	return o.vertex
}

func (o *vertexOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(o.index)}, nil
}

// Compile-time interface checks.
var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
