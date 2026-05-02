// Package sourceop implements the SourceOp vertex for LLB graph construction.
// It provides constructors for all source types: images, git repos, local dirs,
// HTTP resources, and OCI layouts.
package sourceop

import (
	"context"
	"path"
	"strconv"
	"strings"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─────────────────────────────────────────────────────────────────────────────
// SourceOp
// ─────────────────────────────────────────────────────────────────────────────

// SourceOp is a vertex that introduces content into the LLB graph from an
// external source. The source type is determined by the id prefix
// (e.g. "docker-image://", "git://", "local://", "https://").
type SourceOp struct {
	cache       llb.MarshalCache
	id          string
	attrs       map[string]string
	output      llb.Output
	constraints llb.Constraints
	err         error
}

var _ llb.Vertex = (*SourceOp)(nil)

// NewSource creates a generic SourceOp with the given id and attributes.
func NewSource(id string, attrs map[string]string, c llb.Constraints) *SourceOp {
	op := &SourceOp{
		id:          id,
		attrs:       attrs,
		constraints: c,
	}
	op.output = llb.NewOutput(op)
	return op
}

// Validate checks that the source has a non-empty identifier.
func (s *SourceOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if s.err != nil {
		return s.err
	}
	if s.id == "" {
		return errEmptySourceID
	}
	return nil
}

// Marshal serializes the SourceOp into a pb.Op with a pb.SourceOp payload.
func (s *SourceOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := s.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := s.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	pop, md := llb.MarshalConstraints(constraints, &s.constraints)
	pop.Op = &pb.Op_Source{
		Source: &pb.SourceOp{
			Identifier: s.id,
			Attrs:      s.attrs,
		},
	}

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, s.constraints.SourceLocations, constraints)
}

// Output returns the single output of this source.
func (s *SourceOp) Output() llb.Output { return s.output }

// Inputs returns nil — sources have no upstream inputs.
func (s *SourceOp) Inputs() []llb.Output { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Image
// ─────────────────────────────────────────────────────────────────────────────

// ImageOption configures Image source construction.
type ImageOption func(*ImageInfo)

// ImageInfo holds configuration for an image source.
type ImageInfo struct {
	llb.Constraints
	ResolveMode   ResolveMode
	ResolveDigest bool
	LayerLimit    *int
}

// ResolveMode controls image resolution strategy.
type ResolveMode int

const (
	ResolveModeDefault ResolveMode = iota
	ResolveModeForcePull
	ResolveModePreferLocal
)

// String returns the string representation of the resolve mode.
func (r ResolveMode) String() string {
	switch r {
	case ResolveModeForcePull:
		return "pull"
	case ResolveModePreferLocal:
		return "local"
	default:
		return "default"
	}
}

// WithResolveMode sets the image resolution mode.
func WithResolveMode(m ResolveMode) ImageOption {
	return func(ii *ImageInfo) { ii.ResolveMode = m }
}

// WithLayerLimit limits the number of image layers fetched.
func WithLayerLimit(n int) ImageOption {
	return func(ii *ImageInfo) { ii.LayerLimit = &n }
}

// WithImageConstraints applies constraints to the image.
func WithImageConstraints(co llb.ConstraintsOpt) ImageOption {
	return func(ii *ImageInfo) { co.SetConstraintsOption(&ii.Constraints) }
}

// Image returns a State representing a container image from a registry.
func Image(ref string, opts ...ImageOption) llb.State {
	info := &ImageInfo{}
	for _, o := range opts {
		o(info)
	}

	attrs := map[string]string{}
	if info.ResolveMode != ResolveModeDefault {
		attrs[pb.AttrImageResolveMode] = info.ResolveMode.String()
	}
	if info.LayerLimit != nil {
		attrs[pb.AttrImageLayerLimit] = strconv.Itoa(*info.LayerLimit)
	}

	llb.AddCap(&info.Constraints, pb.CapSourceImage)

	src := NewSource("docker-image://"+ref, attrs, info.Constraints)
	return llb.NewState(src.Output())
}

// ─────────────────────────────────────────────────────────────────────────────
// Git
// ─────────────────────────────────────────────────────────────────────────────

// GitOption configures Git source construction.
type GitOption func(*GitInfo)

// GitInfo holds configuration for a git source.
type GitInfo struct {
	llb.Constraints
	KeepGitDir       bool
	AuthTokenSecret  string
	AuthHeaderSecret string
	KnownSSHHosts    string
	MountSSHSocket   string
}

// WithKeepGitDir preserves the .git directory.
func WithKeepGitDir() GitOption {
	return func(gi *GitInfo) { gi.KeepGitDir = true }
}

// WithAuthToken sets the git auth token secret ID.
func WithAuthToken(key string) GitOption {
	return func(gi *GitInfo) { gi.AuthTokenSecret = key }
}

// WithAuthHeader sets the git auth header secret ID.
func WithAuthHeader(key string) GitOption {
	return func(gi *GitInfo) { gi.AuthHeaderSecret = key }
}

// WithGitConstraints applies constraints to the git source.
func WithGitConstraints(co llb.ConstraintsOpt) GitOption {
	return func(gi *GitInfo) { co.SetConstraintsOption(&gi.Constraints) }
}

// Git returns a State representing a git repository checkout.
func Git(url, fragment string, opts ...GitOption) llb.State {
	info := &GitInfo{}
	for _, o := range opts {
		o(info)
	}

	id := "git://" + url
	if fragment != "" {
		id += "#" + fragment
	}

	attrs := map[string]string{}
	if info.KeepGitDir {
		attrs[pb.AttrKeepGitDir] = "true"
	}
	if info.AuthTokenSecret != "" {
		attrs[pb.AttrAuthTokenSecret] = info.AuthTokenSecret
	}
	if info.AuthHeaderSecret != "" {
		attrs[pb.AttrAuthHeaderSecret] = info.AuthHeaderSecret
	}

	llb.AddCap(&info.Constraints, pb.CapSourceGit)

	src := NewSource(id, attrs, info.Constraints)
	return llb.NewState(src.Output())
}

// ─────────────────────────────────────────────────────────────────────────────
// Local
// ─────────────────────────────────────────────────────────────────────────────

// LocalOption configures Local source construction.
type LocalOption func(*LocalInfo)

// LocalInfo holds configuration for a local directory source.
type LocalInfo struct {
	llb.Constraints
	SessionID       string
	IncludePatterns []string
	ExcludePatterns []string
	FollowPaths     []string
	SharedKeyHint   string
}

// WithIncludePatterns restricts the local source to matching patterns.
func WithIncludePatterns(patterns []string) LocalOption {
	return func(li *LocalInfo) { li.IncludePatterns = patterns }
}

// WithExcludePatterns excludes matching patterns from the local source.
func WithExcludePatterns(patterns []string) LocalOption {
	return func(li *LocalInfo) { li.ExcludePatterns = patterns }
}

// WithFollowPaths restricts the local source to specific paths.
func WithFollowPaths(paths []string) LocalOption {
	return func(li *LocalInfo) { li.FollowPaths = paths }
}

// WithSharedKeyHint sets an optimization hint for content-based deduplication.
func WithSharedKeyHint(hint string) LocalOption {
	return func(li *LocalInfo) { li.SharedKeyHint = hint }
}

// WithLocalConstraints applies constraints to the local source.
func WithLocalConstraints(co llb.ConstraintsOpt) LocalOption {
	return func(li *LocalInfo) { co.SetConstraintsOption(&li.Constraints) }
}

// Local returns a State representing a local directory.
func Local(name string, opts ...LocalOption) llb.State {
	info := &LocalInfo{}
	for _, o := range opts {
		o(info)
	}

	attrs := map[string]string{}
	if len(info.IncludePatterns) > 0 {
		attrs[pb.AttrIncludePatterns] = strings.Join(info.IncludePatterns, ",")
	}
	if len(info.ExcludePatterns) > 0 {
		attrs[pb.AttrExcludePatterns] = strings.Join(info.ExcludePatterns, ",")
	}
	if len(info.FollowPaths) > 0 {
		attrs[pb.AttrFollowPaths] = strings.Join(info.FollowPaths, ",")
	}
	if info.SharedKeyHint != "" {
		attrs[pb.AttrSharedKeyHint] = info.SharedKeyHint
	}

	llb.AddCap(&info.Constraints, pb.CapSourceLocal)

	src := NewSource("local://"+name, attrs, info.Constraints)
	return llb.NewState(src.Output())
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP
// ─────────────────────────────────────────────────────────────────────────────

// HTTPOption configures HTTP source construction.
type HTTPOption func(*HTTPInfo)

// HTTPInfo holds configuration for an HTTP source.
type HTTPInfo struct {
	llb.Constraints
	Checksum digest.Digest
	Filename string
	Perm     int
	UID      int
	GID      int
}

// WithChecksum sets the expected digest for the downloaded content.
func WithChecksum(dgst digest.Digest) HTTPOption {
	return func(hi *HTTPInfo) { hi.Checksum = dgst }
}

// WithHTTPFilename sets the filename for the downloaded content.
func WithHTTPFilename(name string) HTTPOption {
	return func(hi *HTTPInfo) { hi.Filename = name }
}

// WithHTTPPerm sets the file permission for the downloaded content.
func WithHTTPPerm(perm int) HTTPOption {
	return func(hi *HTTPInfo) { hi.Perm = perm }
}

// WithHTTPConstraints applies constraints to the HTTP source.
func WithHTTPConstraints(co llb.ConstraintsOpt) HTTPOption {
	return func(hi *HTTPInfo) { co.SetConstraintsOption(&hi.Constraints) }
}

// HTTP returns a State representing a file downloaded via HTTP(S).
func HTTP(url string, opts ...HTTPOption) llb.State {
	info := &HTTPInfo{}
	for _, o := range opts {
		o(info)
	}

	attrs := map[string]string{}
	if info.Checksum != "" {
		attrs[pb.AttrHTTPChecksum] = info.Checksum.String()
	}
	filename := info.Filename
	if filename == "" {
		filename = path.Base(url)
	}
	attrs[pb.AttrHTTPFilename] = filename
	if info.Perm != 0 {
		attrs[pb.AttrHTTPPerm] = "0" + strconv.FormatInt(int64(info.Perm), 8)
	}
	if info.UID != 0 {
		attrs[pb.AttrHTTPUID] = strconv.Itoa(info.UID)
	}
	if info.GID != 0 {
		attrs[pb.AttrHTTPGID] = strconv.Itoa(info.GID)
	}

	llb.AddCap(&info.Constraints, pb.CapSourceHTTP)

	src := NewSource(url, attrs, info.Constraints)
	return llb.NewState(src.Output())
}

// ─────────────────────────────────────────────────────────────────────────────
// OCILayout
// ─────────────────────────────────────────────────────────────────────────────

// OCILayoutOption configures OCI layout source construction.
type OCILayoutOption func(*OCILayoutInfo)

// OCILayoutInfo holds configuration for an OCI layout source.
type OCILayoutInfo struct {
	llb.Constraints
	LayerLimit *int
}

// WithOCILayerLimit limits the number of layers fetched.
func WithOCILayerLimit(n int) OCILayoutOption {
	return func(oi *OCILayoutInfo) { oi.LayerLimit = &n }
}

// WithOCIConstraints applies constraints to the OCI source.
func WithOCIConstraints(co llb.ConstraintsOpt) OCILayoutOption {
	return func(oi *OCILayoutInfo) { co.SetConstraintsOption(&oi.Constraints) }
}

// OCILayout returns a State representing an OCI layout store reference.
func OCILayout(ref string, opts ...OCILayoutOption) llb.State {
	info := &OCILayoutInfo{}
	for _, o := range opts {
		o(info)
	}

	attrs := map[string]string{}
	if info.LayerLimit != nil {
		attrs[pb.AttrImageLayerLimit] = strconv.Itoa(*info.LayerLimit)
	}

	llb.AddCap(&info.Constraints, pb.CapSourceOCILayout)

	src := NewSource("oci-layout://"+ref, attrs, info.Constraints)
	return llb.NewState(src.Output())
}

// ─────────────────────────────────────────────────────────────────────────────
// ImageMetaResolver
// ─────────────────────────────────────────────────────────────────────────────

// ImageMetaResolver resolves image metadata (config, platform) without
// pulling layers. Implementations may use registry APIs or local caches.
type ImageMetaResolver interface {
	ResolveImageConfig(ctx context.Context, ref string, opt ResolveImageConfigOpt) (string, digest.Digest, []byte, error)
}

// ResolveImageConfigOpt provides options for image config resolution.
type ResolveImageConfigOpt struct {
	Platform    *ocispecs.Platform
	ResolveMode string
	LogName     string
}

// ─────────────────────────────────────────────────────────────────────────────
// Errors
// ─────────────────────────────────────────────────────────────────────────────

var errEmptySourceID = sourceError("source identifier must not be empty")

type sourceError string

func (e sourceError) Error() string { return string(e) }
