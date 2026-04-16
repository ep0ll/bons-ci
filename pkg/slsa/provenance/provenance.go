// Package provenance handles capturing build inputs and generating SLSA
// provenance predicates. It has zero external dependencies.
package provenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bons/bons-ci/pkg/slsa/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Capture
// ─────────────────────────────────────────────────────────────────────────────

// Capture accumulates all build inputs observed during a single build
// invocation. Add* methods are idempotent: adding the same source twice is
// equivalent to adding it once.
type Capture struct {
	Frontend            string
	Args                map[string]string
	Sources             types.Sources
	Secrets             []types.Secret
	SSH                 []types.SSH
	NetworkAccess       bool
	IncompleteMaterials bool
}

// ── Image ─────────────────────────────────────────────────────────────────────

// AddImage records a container image as a build input. Duplicates are silently
// dropped (matching Ref + Local + Platform quad).
func (c *Capture) AddImage(i types.ImageSource) {
	for _, v := range c.Sources.Images {
		if v.Ref != i.Ref || v.Local != i.Local {
			continue
		}
		if platformEqual(v.Platform, i.Platform) {
			return
		}
	}
	c.Sources.Images = append(c.Sources.Images, i)
}

// AddImageBlob records a raw image blob.
func (c *Capture) AddImageBlob(i types.ImageBlobSource) {
	for _, v := range c.Sources.ImageBlobs {
		if v.Ref == i.Ref && v.Local == i.Local {
			return
		}
	}
	c.Sources.ImageBlobs = append(c.Sources.ImageBlobs, i)
}

// AddLocal records a named local build context (makes the build non-hermetic).
func (c *Capture) AddLocal(l types.LocalSource) {
	for _, v := range c.Sources.Local {
		if v.Name == l.Name {
			return
		}
	}
	c.Sources.Local = append(c.Sources.Local, l)
}

// AddGit records a Git repository. Credentials are stripped from the URL.
func (c *Capture) AddGit(g types.GitSource) {
	g.URL = RedactCredentials(g.URL)
	for _, v := range c.Sources.Git {
		if v.URL == g.URL {
			return
		}
	}
	c.Sources.Git = append(c.Sources.Git, g)
}

// AddHTTP records an HTTP artifact.
func (c *Capture) AddHTTP(h types.HTTPSource) {
	h.URL = RedactCredentials(h.URL)
	for _, v := range c.Sources.HTTP {
		if v.URL == h.URL {
			return
		}
	}
	c.Sources.HTTP = append(c.Sources.HTTP, h)
}

// ── Secret / SSH ──────────────────────────────────────────────────────────────

// AddSecret records a secret. Optional=false always wins over Optional=true.
func (c *Capture) AddSecret(s types.Secret) {
	for i, v := range c.Secrets {
		if v.ID == s.ID {
			if !s.Optional {
				c.Secrets[i].Optional = false
			}
			return
		}
	}
	c.Secrets = append(c.Secrets, s)
}

// AddSSH records an SSH agent socket. An empty ID is normalised to "default".
func (c *Capture) AddSSH(s types.SSH) {
	if s.ID == "" {
		s.ID = "default"
	}
	for i, v := range c.SSH {
		if v.ID == s.ID {
			if !s.Optional {
				c.SSH[i].Optional = false
			}
			return
		}
	}
	c.SSH = append(c.SSH, s)
}

// ── Merge ─────────────────────────────────────────────────────────────────────

// Merge incorporates all sources from c2. Nil c2 is a no-op.
func (c *Capture) Merge(c2 *Capture) error {
	if c2 == nil {
		return nil
	}
	for _, i := range c2.Sources.Images {
		c.AddImage(i)
	}
	for _, i := range c2.Sources.ImageBlobs {
		c.AddImageBlob(i)
	}
	for _, l := range c2.Sources.Local {
		c.AddLocal(l)
	}
	for _, g := range c2.Sources.Git {
		c.AddGit(g)
	}
	for _, h := range c2.Sources.HTTP {
		c.AddHTTP(h)
	}
	for _, s := range c2.Secrets {
		c.AddSecret(s)
	}
	for _, s := range c2.SSH {
		c.AddSSH(s)
	}
	if c2.NetworkAccess {
		c.NetworkAccess = true
	}
	if c2.IncompleteMaterials {
		c.IncompleteMaterials = true
	}
	return nil
}

// ── Sort / Optimise ───────────────────────────────────────────────────────────

// Sort deterministically orders all source slices so serialised predicates are
// stable across equivalent builds.
func (c *Capture) Sort() {
	sort.Slice(c.Sources.Images, func(i, j int) bool {
		return c.Sources.Images[i].Ref < c.Sources.Images[j].Ref
	})
	sort.Slice(c.Sources.ImageBlobs, func(i, j int) bool {
		return c.Sources.ImageBlobs[i].Ref < c.Sources.ImageBlobs[j].Ref
	})
	sort.Slice(c.Sources.Local, func(i, j int) bool {
		return c.Sources.Local[i].Name < c.Sources.Local[j].Name
	})
	sort.Slice(c.Sources.Git, func(i, j int) bool {
		return c.Sources.Git[i].URL < c.Sources.Git[j].URL
	})
	sort.Slice(c.Sources.HTTP, func(i, j int) bool {
		return c.Sources.HTTP[i].URL < c.Sources.HTTP[j].URL
	})
	sort.Slice(c.Secrets, func(i, j int) bool {
		return c.Secrets[i].ID < c.Secrets[j].ID
	})
	sort.Slice(c.SSH, func(i, j int) bool {
		return c.SSH[i].ID < c.SSH[j].ID
	})
}

// OptimizeImageSources removes digest-only image references when a
// corresponding tag reference is already recorded for the same image.
func (c *Capture) OptimizeImageSources() error {
	tagged := map[string]struct{}{}
	for _, img := range c.Sources.Images {
		name, tag, isDigest := parseImageRef(img.Ref)
		if !isDigest {
			tagged[name+":"+tag] = struct{}{}
		}
	}
	filtered := c.Sources.Images[:0]
	for _, img := range c.Sources.Images {
		name, tag, isDigest := parseImageRef(img.Ref)
		if isDigest {
			if _, exists := tagged[name+":"+tag]; exists {
				continue // drop: tag ref already present
			}
		}
		filtered = append(filtered, img)
	}
	c.Sources.Images = filtered
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// IsHermetic reports whether the capture represents a hermetic build.
func (c *Capture) IsHermetic() bool {
	return !c.NetworkAccess && !c.IncompleteMaterials && len(c.Sources.Local) == 0
}

// HasIncompleteMaterials reports whether the dependency graph is incomplete.
func (c *Capture) HasIncompleteMaterials() bool {
	return c.IncompleteMaterials || len(c.Sources.Local) > 0
}

// RedactCredentials strips user:password from a URL string.
func RedactCredentials(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}

func platformEqual(a, b *types.Platform) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.OS == b.OS && a.Architecture == b.Architecture &&
		a.Variant == b.Variant && a.OSVersion == b.OSVersion
}

// parseImageRef splits an image reference into (name, tag-or-latest, isDigest).
// isDigest is true when the reference uses a @sha256:… digest, not a tag.
func parseImageRef(ref string) (name, tag string, isDigest bool) {
	if idx := strings.Index(ref, "@"); idx != -1 {
		return ref[:idx], "latest", true
	}
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		return ref[:idx], ref[idx+1:], false
	}
	return ref, "latest", false
}

// ─────────────────────────────────────────────────────────────────────────────
// SLSA v1 predicate types
// ─────────────────────────────────────────────────────────────────────────────

// PredicateV1 is the SLSA 1.0 provenance predicate.
type PredicateV1 struct {
	BuildDefinition BuildDefinitionV1 `json:"buildDefinition"`
	RunDetails      RunDetailsV1      `json:"runDetails"`
}

// BuildDefinitionV1 holds the build type, external parameters and internal parameters.
type BuildDefinitionV1 struct {
	BuildType            string           `json:"buildType"`
	ExternalParameters   ExternalParamsV1 `json:"externalParameters"`
	InternalParameters   InternalParamsV1 `json:"internalParameters"`
	ResolvedDependencies []Material       `json:"resolvedDependencies,omitempty"`
}

// Material is an alias for types.Material to allow local embedding.
type Material = types.Material

// ExternalParamsV1 holds user-visible build parameters.
type ExternalParamsV1 struct {
	ConfigSource ConfigSourceV1        `json:"configSource,omitempty"`
	Request      types.BuildParameters `json:"request"`
}

// ConfigSourceV1 identifies the build instructions source.
type ConfigSourceV1 struct {
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
	Path   string            `json:"path,omitempty"`
}

// InternalParamsV1 holds build-platform-controlled parameters.
type InternalParamsV1 struct {
	BuildConfig     *types.BuildConfig `json:"buildConfig,omitempty"`
	BuilderPlatform string             `json:"builderPlatform,omitempty"`
	CustomEnv       map[string]any     `json:"customEnv,omitempty"`
}

// RunDetailsV1 holds builder identity and build metadata.
type RunDetailsV1 struct {
	Builder  types.BuilderInfo `json:"builder"`
	Metadata *MetadataV1       `json:"metadata,omitempty"`
}

// MetadataV1 extends build metadata with BuildKit-specific fields.
type MetadataV1 struct {
	InvocationID string             `json:"invocationID,omitempty"`
	StartedOn    *time.Time         `json:"startedOn,omitempty"`
	FinishedOn   *time.Time         `json:"finishedOn,omitempty"`
	Hermetic     bool               `json:"buildkit_hermetic,omitempty"`
	Completeness types.Completeness `json:"buildkit_completeness"`
	Reproducible bool               `json:"buildkit_reproducible,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// SLSA v0.2 predicate types
// ─────────────────────────────────────────────────────────────────────────────

// PredicateV02 is the SLSA 0.2 provenance predicate (legacy).
type PredicateV02 struct {
	Builder     BuilderV02         `json:"builder"`
	BuildType   string             `json:"buildType"`
	Invocation  InvocationV02      `json:"invocation"`
	BuildConfig *types.BuildConfig `json:"buildConfig,omitempty"`
	Materials   []Material         `json:"materials,omitempty"`
	Metadata    *MetadataV02       `json:"metadata,omitempty"`
}

// BuilderV02 is the SLSA v0.2 builder block.
type BuilderV02 struct {
	ID string `json:"id"`
}

// InvocationV02 holds the v0.2 invocation block.
type InvocationV02 struct {
	ConfigSource ConfigSourceV02       `json:"configSource"`
	Parameters   types.BuildParameters `json:"parameters"`
	Environment  EnvironmentV02        `json:"environment"`
}

// ConfigSourceV02 is the SLSA v0.2 config source.
type ConfigSourceV02 struct {
	URI        string            `json:"uri,omitempty"`
	Digest     map[string]string `json:"digest,omitempty"`
	EntryPoint string            `json:"entryPoint,omitempty"`
}

// EnvironmentV02 records the build environment.
type EnvironmentV02 struct {
	Platform string `json:"platform,omitempty"`
}

// MetadataV02 extends build metadata for v0.2.
type MetadataV02 struct {
	BuildInvocationID string             `json:"buildInvocationID,omitempty"`
	BuildStartedOn    *time.Time         `json:"buildStartedOn,omitempty"`
	BuildFinishedOn   *time.Time         `json:"buildFinishedOn,omitempty"`
	Completeness      types.Completeness `json:"completeness"`
	Hermetic          bool               `json:"buildkit_hermetic,omitempty"`
	Reproducible      bool               `json:"reproducible,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// NewPredicateV1
// ─────────────────────────────────────────────────────────────────────────────

// NewPredicateV1 converts the given Capture into a SLSA v1 provenance predicate.
func NewPredicateV1(c *Capture) (*PredicateV1, error) {
	if c == nil {
		return nil, errors.New("provenance: capture must not be nil")
	}

	deps, err := buildMaterials(c.Sources)
	if err != nil {
		return nil, fmt.Errorf("provenance: build materials: %w", err)
	}

	args := cloneMap(c.Args)
	contextKey := resolveContextKey(args)

	ext := ExternalParamsV1{}
	if v, ok := args[contextKey]; ok && v != "" {
		ext.ConfigSource.URI = RedactCredentials(v)
		delete(args, contextKey)
	}
	if v, ok := args["filename"]; ok && v != "" {
		ext.ConfigSource.Path = v
		delete(args, "filename")
	}
	ext.Request = types.BuildParameters{
		Frontend: c.Frontend,
		Args:     filterArgs(args),
	}
	for i := range c.Secrets {
		s := c.Secrets[i]
		ext.Request.Secrets = append(ext.Request.Secrets, &s)
	}
	for i := range c.SSH {
		s := c.SSH[i]
		ext.Request.SSH = append(ext.Request.SSH, &s)
	}
	for i := range c.Sources.Local {
		l := c.Sources.Local[i]
		ext.Request.Locals = append(ext.Request.Locals, &l)
	}

	p := &PredicateV1{
		BuildDefinition: BuildDefinitionV1{
			BuildType:            types.BuildTypeGenericV1,
			ExternalParameters:   ext,
			ResolvedDependencies: deps,
		},
		RunDetails: RunDetailsV1{
			Metadata: &MetadataV1{
				Hermetic: c.IsHermetic(),
				Completeness: types.Completeness{
					Parameters:  c.Frontend != "",
					Environment: true,
					Materials:   !c.HasIncompleteMaterials(),
				},
			},
		},
	}
	return p, nil
}

// ─── PredicateV1 setters ──────────────────────────────────────────────────────

// SetBuilder sets the builder URI and optional version map.
func (p *PredicateV1) SetBuilder(id string, version map[string]string) {
	p.RunDetails.Builder.ID = id
	p.RunDetails.Builder.Version = version
}

// SetBuildType overrides the build type URI.
func (p *PredicateV1) SetBuildType(bt string) {
	p.BuildDefinition.BuildType = bt
}

// SetInvocationID sets a unique build invocation identifier.
func (p *PredicateV1) SetInvocationID(id string) {
	p.ensureMetadata().InvocationID = id
}

// SetTimes records the build start and finish timestamps.
func (p *PredicateV1) SetTimes(started, finished time.Time) {
	m := p.ensureMetadata()
	m.StartedOn = &started
	m.FinishedOn = &finished
}

// SetReproducible marks (or unmarks) the build as reproducible.
func (p *PredicateV1) SetReproducible(v bool) {
	p.ensureMetadata().Reproducible = v
}

// SetHermetic overrides the hermetic flag.
func (p *PredicateV1) SetHermetic(v bool) {
	p.ensureMetadata().Hermetic = v
}

// SetBuildConfig embeds the resolved build graph.
func (p *PredicateV1) SetBuildConfig(bc *types.BuildConfig) {
	p.BuildDefinition.InternalParameters.BuildConfig = bc
}

// SetCustomEnv merges arbitrary key-value pairs into InternalParameters.
func (p *PredicateV1) SetCustomEnv(env map[string]any) {
	if p.BuildDefinition.InternalParameters.CustomEnv == nil {
		p.BuildDefinition.InternalParameters.CustomEnv = map[string]any{}
	}
	maps.Copy(p.BuildDefinition.InternalParameters.CustomEnv, env)
}

// SetBuilderPlatform records the build-platform string (e.g. "linux/amd64").
func (p *PredicateV1) SetBuilderPlatform(platform string) {
	p.BuildDefinition.InternalParameters.BuilderPlatform = platform
}

func (p *PredicateV1) ensureMetadata() *MetadataV1 {
	if p.RunDetails.Metadata == nil {
		p.RunDetails.Metadata = &MetadataV1{}
	}
	return p.RunDetails.Metadata
}

// MarshalJSON serialises the predicate to JSON.
func (p *PredicateV1) MarshalJSON() ([]byte, error) {
	type alias PredicateV1
	return json.Marshal((*alias)(p))
}

// ─────────────────────────────────────────────────────────────────────────────
// ConvertToV02
// ─────────────────────────────────────────────────────────────────────────────

// ConvertToV02 converts a v1 predicate to the legacy SLSA v0.2 format.
func ConvertToV02(p *PredicateV1) *PredicateV02 {
	var meta *MetadataV02
	if p.RunDetails.Metadata != nil {
		m := p.RunDetails.Metadata
		meta = &MetadataV02{
			BuildInvocationID: m.InvocationID,
			BuildStartedOn:    m.StartedOn,
			BuildFinishedOn:   m.FinishedOn,
			Completeness:      m.Completeness,
			Hermetic:          m.Hermetic,
			Reproducible:      m.Reproducible,
		}
	}
	return &PredicateV02{
		Builder:   BuilderV02{ID: p.RunDetails.Builder.ID},
		BuildType: types.BuildTypeBuildKitV02,
		Invocation: InvocationV02{
			ConfigSource: ConfigSourceV02{
				URI:        p.BuildDefinition.ExternalParameters.ConfigSource.URI,
				Digest:     p.BuildDefinition.ExternalParameters.ConfigSource.Digest,
				EntryPoint: p.BuildDefinition.ExternalParameters.ConfigSource.Path,
			},
			Parameters:  p.BuildDefinition.ExternalParameters.Request,
			Environment: EnvironmentV02{Platform: p.BuildDefinition.InternalParameters.BuilderPlatform},
		},
		BuildConfig: p.BuildDefinition.InternalParameters.BuildConfig,
		Materials:   p.BuildDefinition.ResolvedDependencies,
		Metadata:    meta,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Material / digest helpers
// ─────────────────────────────────────────────────────────────────────────────

func buildMaterials(srcs types.Sources) ([]Material, error) {
	var out []Material
	for _, s := range srcs.Images {
		uri := "docker://" + s.Ref
		if s.Local {
			uri = "oci-layout://" + s.Ref
		}
		m := Material{URI: uri}
		if s.Digest != "" {
			m.Digest = splitDigest(s.Digest)
		}
		out = append(out, m)
	}
	for _, s := range srcs.ImageBlobs {
		uri := "oci://" + s.Ref
		if s.Local {
			uri = "oci-layout://" + s.Ref
		}
		m := Material{URI: uri}
		if s.Digest != "" {
			m.Digest = splitDigest(s.Digest)
		}
		out = append(out, m)
	}
	for _, s := range srcs.Git {
		out = append(out, Material{
			URI:    s.URL,
			Digest: digestSetForCommit(s.Commit),
		})
	}
	for _, s := range srcs.HTTP {
		m := Material{URI: s.URL}
		if s.Digest != "" {
			m.Digest = splitDigest(s.Digest)
		}
		out = append(out, m)
	}
	return out, nil
}

// splitDigest converts "sha256:abc…" into map{"sha256":"abc…"}.
func splitDigest(d string) map[string]string {
	if idx := strings.Index(d, ":"); idx != -1 {
		return map[string]string{d[:idx]: d[idx+1:]}
	}
	return map[string]string{"sha256": d}
}

// digestSetForCommit creates a digest map for a Git commit hash.
// 64-hex chars = SHA-256, otherwise treat as SHA-1.
func digestSetForCommit(commit string) map[string]string {
	if commit == "" {
		return nil
	}
	if len(commit) == 64 {
		return map[string]string{"sha256": commit}
	}
	return map[string]string{"sha1": commit}
}

// ─────────────────────────────────────────────────────────────────────────────
// FilterArgs / helpers
// ─────────────────────────────────────────────────────────────────────────────

var hostSpecificArgs = map[string]bool{
	"cgroup-parent":      true,
	"image-resolve-mode": true,
	"platform":           true,
	"cache-imports":      true,
}

// FilterArgs removes host-specific and attestation arguments from a map.
// The returned map is a new allocation; the original is not modified.
func FilterArgs(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if hostSpecificArgs[k] {
			continue
		}
		if strings.HasPrefix(k, "attest:") {
			continue
		}
		if k == "context" || strings.HasPrefix(k, "context:") {
			v = RedactCredentials(v)
		}
		out[k] = v
	}
	return out
}

func filterArgs(m map[string]string) map[string]string { return FilterArgs(m) }

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

func resolveContextKey(args map[string]string) string {
	if v, ok := args["contextkey"]; ok && v != "" {
		return v
	}
	return "context"
}
