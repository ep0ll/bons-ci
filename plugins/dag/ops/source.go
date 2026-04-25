package ops

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── SourceOp ─────────────────────────────────────────────────────────────────

// SourceOp represents any operation that fetches content from an external origin.
// It has no inputs (it IS the root of the dependency tree) and produces one output.
//
// The Identifier field encodes the source type and location:
//   - "docker-image://docker.io/library/alpine:latest" — OCI/Docker image
//   - "git://github.com/foo/bar.git#v1.0"             — Git repository
//   - "https://example.com/file.tar.gz"               — HTTP download
//   - "local://my-context"                            — client-local directory
//   - "oci-layout://myrepo@sha256:abc123"             — OCI layout store
type SourceOp struct {
	id          string
	identifier  string
	attrs       map[string]string
	constraints Constraints
	name        string
}

var _ vertex.Vertex = (*SourceOp)(nil)
var _ vertex.Named = (*SourceOp)(nil)
var _ vertex.Described = (*SourceOp)(nil)

func newSourceOp(identifier string, attrs map[string]string, c Constraints) *SourceOp {
	s := &SourceOp{
		identifier:  identifier,
		attrs:       cloneStringMap(attrs),
		constraints: c,
	}
	s.id = idOf(struct {
		Kind       string      `json:"kind"`
		Identifier string      `json:"identifier"`
		Attrs      [][2]string `json:"attrs"`
		Platform   *Platform   `json:"platform,omitempty"`
	}{
		Kind:       string(vertex.KindSource),
		Identifier: identifier,
		Attrs:      attrsSlice(attrs),
		Platform:   c.Platform,
	})
	s.name = identifier
	return s
}

func (s *SourceOp) ID() string                     { return s.id }
func (s *SourceOp) Kind() vertex.Kind              { return vertex.KindSource }
func (s *SourceOp) Inputs() []vertex.Vertex        { return nil }
func (s *SourceOp) Name() string                   { return s.name }
func (s *SourceOp) Identifier() string             { return s.identifier }
func (s *SourceOp) Attrs() map[string]string       { return s.attrs }
func (s *SourceOp) Constraints() Constraints       { return s.constraints }
func (s *SourceOp) Description() map[string]string { return s.constraints.Description }
func (s *SourceOp) Ref() vertex.Ref                { return vertex.Ref{Vertex: s, Index: 0} }

func (s *SourceOp) Validate(_ context.Context) error {
	if s.identifier == "" {
		return fmt.Errorf("source: identifier must not be empty")
	}
	return nil
}

// ─── Image ────────────────────────────────────────────────────────────────────

// ImageInfo carries options for constructing an image source.
type ImageInfo struct {
	Constraints
	// ResolveMode controls how the image reference is resolved.
	ResolveMode ImageResolveMode
	// RecordType is an opaque tag for internal classification.
	RecordType string
	// LayerLimit caps the number of layers the solver may pull.
	LayerLimit *int
}

// ImageResolveMode controls reference resolution policy.
type ImageResolveMode int

const (
	ResolveModeDefault     ImageResolveMode = iota // solver's default heuristics
	ResolveModeForcePull                           // always fetch from registry
	ResolveModePreferLocal                         // use local cache if available
)

func (r ImageResolveMode) String() string {
	switch r {
	case ResolveModeForcePull:
		return "pull"
	case ResolveModePreferLocal:
		return "local"
	default:
		return "default"
	}
}

// Image returns a SourceOp that fetches an OCI/Docker image from a registry.
// The ref is normalised to a fully-qualified name (e.g. "alpine" →
// "docker.io/library/alpine:latest"). See normalizeImageRef for the rules.
func Image(ref string, opts ...func(*ImageInfo)) *SourceOp {
	ref = normalizeImageRef(ref)

	info := &ImageInfo{}
	for _, o := range opts {
		o(info)
	}

	attrs := make(map[string]string)
	if info.ResolveMode != ResolveModeDefault {
		attrs["image.resolvemode"] = info.ResolveMode.String()
	}
	if info.RecordType != "" {
		attrs["image.recordtype"] = info.RecordType
	}
	if info.LayerLimit != nil {
		attrs["image.layerlimit"] = fmt.Sprintf("%d", *info.LayerLimit)
	}

	src := newSourceOp("docker-image://"+ref, attrs, info.Constraints)
	src.name = "image:" + ref
	return src
}

func WithImageResolveMode(m ImageResolveMode) func(*ImageInfo) {
	return func(i *ImageInfo) { i.ResolveMode = m }
}

func WithImageLayerLimit(n int) func(*ImageInfo) {
	return func(i *ImageInfo) { i.LayerLimit = &n }
}

// ─── Git ──────────────────────────────────────────────────────────────────────

type GitInfo struct {
	Constraints
	Ref              string
	SubDir           string
	KeepGitDir       bool
	AuthTokenSecret  string
	AuthHeaderSecret string
	KnownSSHHosts    string
	Checksum         string
	SkipSubmodules   bool
}

// Git returns a SourceOp that clones a Git repository.
func Git(url string, opts ...func(*GitInfo)) *SourceOp {
	info := &GitInfo{
		AuthTokenSecret:  "GIT_AUTH_TOKEN",
		AuthHeaderSecret: "GIT_AUTH_HEADER",
	}
	for _, o := range opts {
		o(info)
	}

	identifier := "git://" + url
	if info.Ref != "" {
		identifier += "#" + info.Ref
		if info.SubDir != "" {
			identifier += ":" + info.SubDir
		}
	}

	attrs := make(map[string]string)
	attrs["git.fullurl"] = url
	if info.KeepGitDir {
		attrs["git.keepgitdir"] = "true"
	}
	if info.AuthTokenSecret != "" {
		attrs["git.authtokensecret"] = info.AuthTokenSecret
	}
	if info.AuthHeaderSecret != "" {
		attrs["git.authheadersecret"] = info.AuthHeaderSecret
	}
	if info.KnownSSHHosts != "" {
		attrs["git.knownsshosts"] = info.KnownSSHHosts
	}
	if info.Checksum != "" {
		attrs["git.checksum"] = info.Checksum
	}
	if info.SkipSubmodules {
		attrs["git.skipsubmodules"] = "true"
	}

	src := newSourceOp(identifier, attrs, info.Constraints)
	src.name = "git:" + url
	return src
}

func WithGitRef(ref string) func(*GitInfo)    { return func(g *GitInfo) { g.Ref = ref } }
func WithGitSubDir(dir string) func(*GitInfo) { return func(g *GitInfo) { g.SubDir = dir } }
func WithGitKeepDir() func(*GitInfo)          { return func(g *GitInfo) { g.KeepGitDir = true } }

// ─── HTTP ─────────────────────────────────────────────────────────────────────

type HTTPInfo struct {
	Constraints
	Checksum string
	Filename string
	Perm     int
	UID      int
	GID      int
}

// HTTP returns a SourceOp that downloads a file over HTTP(S).
func HTTP(url string, opts ...func(*HTTPInfo)) *SourceOp {
	info := &HTTPInfo{}
	for _, o := range opts {
		o(info)
	}

	attrs := make(map[string]string)
	if info.Checksum != "" {
		attrs["http.checksum"] = info.Checksum
	}
	if info.Filename != "" {
		attrs["http.filename"] = info.Filename
	}
	if info.Perm != 0 {
		attrs["http.perm"] = fmt.Sprintf("%o", info.Perm)
	}
	if info.UID != 0 {
		attrs["http.uid"] = fmt.Sprintf("%d", info.UID)
	}
	if info.GID != 0 {
		attrs["http.gid"] = fmt.Sprintf("%d", info.GID)
	}

	src := newSourceOp(url, attrs, info.Constraints)
	src.name = "http:" + url
	return src
}

func WithHTTPChecksum(dgst string) func(*HTTPInfo) {
	return func(h *HTTPInfo) { h.Checksum = dgst }
}

// ─── Local ────────────────────────────────────────────────────────────────────

type LocalInfo struct {
	Constraints
	SessionID       string
	IncludePatterns []string
	ExcludePatterns []string
	FollowPaths     []string
	SharedKeyHint   string
}

// Local returns a SourceOp that reads a directory from the client filesystem.
// name is a logical name for the context (e.g. "context", "dockerfile").
func Local(name string, opts ...func(*LocalInfo)) *SourceOp {
	info := &LocalInfo{}
	for _, o := range opts {
		o(info)
	}

	attrs := make(map[string]string)
	if info.SessionID != "" {
		attrs["local.session"] = info.SessionID
	}
	if len(info.IncludePatterns) > 0 {
		attrs["local.includepatterns"] = strings.Join(info.IncludePatterns, ",")
	}
	if len(info.ExcludePatterns) > 0 {
		attrs["local.excludepatterns"] = strings.Join(info.ExcludePatterns, ",")
	}
	if info.SharedKeyHint != "" {
		attrs["local.sharedkeyhint"] = info.SharedKeyHint
	}

	src := newSourceOp("local://"+name, attrs, info.Constraints)
	src.name = "local:" + name
	return src
}

func WithLocalSession(id string) func(*LocalInfo) {
	return func(l *LocalInfo) { l.SessionID = id }
}
func WithLocalInclude(patterns ...string) func(*LocalInfo) {
	return func(l *LocalInfo) { l.IncludePatterns = append(l.IncludePatterns, patterns...) }
}
func WithLocalExclude(patterns ...string) func(*LocalInfo) {
	return func(l *LocalInfo) { l.ExcludePatterns = append(l.ExcludePatterns, patterns...) }
}

// ─── OCILayout ────────────────────────────────────────────────────────────────

type OCILayoutInfo struct {
	Constraints
	SessionID  string
	StoreID    string
	LayerLimit *int
}

// OCILayout returns a SourceOp that reads from an OCI image layout store.
func OCILayout(ref string, opts ...func(*OCILayoutInfo)) *SourceOp {
	info := &OCILayoutInfo{}
	for _, o := range opts {
		o(info)
	}
	attrs := make(map[string]string)
	if info.SessionID != "" {
		attrs["oci.session"] = info.SessionID
	}
	if info.StoreID != "" {
		attrs["oci.store"] = info.StoreID
	}
	src := newSourceOp("oci-layout://"+ref, attrs, info.Constraints)
	src.name = "oci:" + ref
	return src
}

// ─── Scratch ──────────────────────────────────────────────────────────────────

// Scratch returns a zero-value Ref representing an empty filesystem.
func Scratch() vertex.Ref { return vertex.Ref{} }

// ─── Image ref normalization ──────────────────────────────────────────────────

// normalizeImageRef converts a short image reference to a fully-qualified form
// matching the behaviour of github.com/distribution/reference.ParseNormalizedNamed
// without requiring that dependency.
//
// Rules (applied in order):
//  1. If the first path component looks like a hostname (contains ".", ":", or
//     equals "localhost"), leave the registry as-is.
//  2. If the ref contains no "/" at all, add "docker.io/library/" prefix.
//  3. Otherwise add "docker.io/" prefix (user/image style, no explicit registry).
//  4. If the final path component contains neither ":" nor "@", append ":latest".
//
// A hostname component is: contains "." OR ":" OR equals "localhost".
// This matches the heuristic used by containerd/distribution.
func normalizeImageRef(ref string) string {
	// Step 1-3: determine if a registry prefix is needed.
	if !strings.Contains(ref, "/") {
		// Plain name like "alpine" → library image.
		ref = "docker.io/library/" + ref
	} else {
		first := strings.SplitN(ref, "/", 2)[0]
		if !isRegistryHost(first) {
			// "user/image" style → add docker.io/.
			ref = "docker.io/" + ref
		}
		// else: already has a registry host, leave as-is.
	}

	// Step 4: append :latest if no tag or digest is present.
	// Examine only the last path segment to avoid false positives from
	// ports in the hostname (e.g. "myhost:5000/img" should still get :latest).
	last := path.Base(ref)
	// Strip any digest (@sha256:...) from the segment before checking for a tag.
	withoutDigest := last
	if at := strings.IndexByte(last, '@'); at >= 0 {
		withoutDigest = last[:at]
	}
	if !strings.Contains(withoutDigest, ":") && !strings.Contains(ref, "@") {
		ref = ref + ":latest"
	}

	return ref
}

// isRegistryHost returns true if the given first path component looks like a
// container registry hostname (as opposed to a Docker Hub username).
//
// Heuristic: the component contains "." (e.g. "gcr.io", "docker.io") OR
// ":" (e.g. "localhost:5000", "myregistry:5000") OR equals "localhost".
//
// This matches the behaviour of github.com/distribution/reference.
func isRegistryHost(component string) bool {
	return component == "localhost" ||
		strings.ContainsAny(component, ".:")
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
