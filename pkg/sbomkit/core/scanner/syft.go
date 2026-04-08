// Package scanner provides a Syft-backed implementation of ports.Scanner.
package scanner

import (
	"context"
	"strings"
	"sync"
	"time"

	stereoscopeimage "github.com/anchore/stereoscope/pkg/image"
	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/artifact"
	"github.com/anchore/syft/syft/cataloging"
	"github.com/anchore/syft/syft/pkg"
	syftsbom "github.com/anchore/syft/syft/sbom"
	syftSource "github.com/anchore/syft/syft/source"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

const (
	toolVendor  = "yourorg"
	toolName    = "sbomkit"
	toolVersion = "v0.1.0"
)

// SyftScanner wraps the Syft library and implements ports.Scanner.
//
// It is safe for concurrent use: each Scan call is independent and shares no
// mutable state. The zero value is not useful; use NewSyftScanner.
type SyftScanner struct {
	logger *zap.Logger
	opts   SyftOptions
	once   sync.Once
}

// SyftOptions tunes Syft scanner behaviour.
type SyftOptions struct {
	// DefaultImagePullSource selects where to pull images from.
	// Valid values: "docker" (Docker daemon), "registry" (direct OCI registry pull).
	// Default: "registry".
	DefaultImagePullSource string

	// Parallelism is the max number of concurrent syft catalogers.
	// 0 uses runtime.NumCPU().
	Parallelism int
}

// DefaultSyftOptions returns sensible production defaults.
func DefaultSyftOptions() SyftOptions {
	return SyftOptions{
		DefaultImagePullSource: "registry",
		Parallelism:            0,
	}
}

// NewSyftScanner constructs a SyftScanner.
func NewSyftScanner(logger *zap.Logger, opts SyftOptions) *SyftScanner {
	if opts.DefaultImagePullSource == "" {
		opts.DefaultImagePullSource = "registry"
	}
	return &SyftScanner{logger: logger, opts: opts}
}

// Name implements ports.Scanner.
func (s *SyftScanner) Name() string { return "syft" }

// Close implements ports.Scanner. Syft holds no persistent resources, so this
// is a no-op beyond ensuring idempotency via sync.Once.
func (s *SyftScanner) Close() error {
	s.once.Do(func() {}) // idempotency guard; nothing to release
	return nil
}

// Scan implements ports.Scanner.
//
// Pipeline:
//  1. Build syft GetSourceConfig from domain.Source.
//  2. Call syft.GetSource to obtain a source.Source.
//  3. Call syft.CreateSBOM to catalog packages and build relationships.
//  4. Convert to domain types for cross-exporter compatibility, storing the
//     raw *syftsbom.SBOM in domain.SBOM.Raw for high-fidelity export.
func (s *SyftScanner) Scan(ctx context.Context, src domain.Source, opts ports.ScanOptions) (*domain.SBOM, error) {
	s.logger.Info("syft scan starting",
		zap.String("source", src.Identifier),
		zap.String("kind", string(src.Kind)),
	)

	// ── 1. Build syft source config ──────────────────────────────────────────
	userInput, err := toSyftInput(src)
	if err != nil {
		return nil, domain.Newf(domain.ErrKindValidation, err, "building syft input for %q", src.Identifier)
	}

	syftCfg := s.buildGetSourceConfig(src, opts)

	// ── 2. Resolve source ────────────────────────────────────────────────────
	syftSrc, err := syft.GetSource(ctx, userInput, syftCfg)
	if err != nil {
		return nil, domain.Newf(domain.ErrKindScanning, err, "syft failed to open source %q", src.Identifier)
	}
	defer func() {
		if closeErr := syftSrc.Close(); closeErr != nil {
			s.logger.Warn("error closing syft source", zap.Error(closeErr))
		}
	}()

	// ── 3. Catalog packages ──────────────────────────────────────────────────
	// syft.CreateSBOM returns a fully assembled *syftsbom.SBOM with Packages,
	// Relationships, and LinuxDistribution already populated.
	createCfg := s.buildCreateSBOMConfig(opts)
	raw, err := syft.CreateSBOM(ctx, syftSrc, createCfg)
	if err != nil {
		return nil, domain.Newf(domain.ErrKindScanning, err, "syft cataloging failed for %q", src.Identifier)
	}

	collection := raw.Artifacts.Packages

	s.logger.Info("syft cataloging complete",
		zap.String("source", src.Identifier),
		zap.Int("packages", collection.PackageCount()),
		zap.Int("relationships", len(raw.Relationships)),
	)

	// ── 4. Convert to domain types ───────────────────────────────────────────
	components := make([]domain.Component, 0, collection.PackageCount())
	for p := range collection.Enumerate() {
		components = append(components, toComponent(p))
	}

	sbom := &domain.SBOM{
		ID:            string(syftSrc.ID()),
		Source:        src,
		Components:    components,
		Relationships: toRelationships(raw.Relationships),
		Metadata: domain.Metadata{
			Tool: domain.Tool{
				Vendor:  toolVendor,
				Name:    toolName,
				Version: toolVersion,
			},
			Lifecycle: domain.LifecycleBuild,
		},
		Format:      domain.FormatCycloneDXJSON,
		GeneratedAt: time.Now().UTC(),
		Raw:         raw, // *syftsbom.SBOM; consumed by high-fidelity exporters
	}

	return sbom, nil
}

// ── private helpers ──────────────────────────────────────────────────────────

// toSyftInput converts a domain.Source to the user-input string syft expects.
//
//	image      → bare image reference (e.g. "docker.io/ubuntu:22.04")
//	snapshot   → "dir:/absolute/path"
//	directory  → "dir:/absolute/path"
//	archive    → "/path/to/image.tar"
//	oci-layout → "oci-dir:/path"
func toSyftInput(src domain.Source) (string, error) {
	switch src.Kind {
	case domain.SourceImage:
		return src.Identifier, nil
	case domain.SourceSnapshot, domain.SourceDirectory:
		return "dir:" + src.Identifier, nil
	case domain.SourceArchive:
		return src.Identifier, nil
	case domain.SourceOCILayout:
		return "oci-dir:" + src.Identifier, nil
	default:
		return "", domain.Newf(domain.ErrKindValidation, nil, "unsupported source kind %q", src.Kind)
	}
}

// buildGetSourceConfig maps domain options to syft's GetSourceConfig.
//
// API notes (syft v1.42.x):
//   - Exclusion paths are passed via WithExcludeConfig(source.ExcludeConfig{Paths: ...}).
//     The old WithExcludes(...string) variadic helper was removed in v1.x.
//   - WithDefaultImagePullSource is the preferred single-source setter;
//     WithSources remains available for multi-source ordered lists.
func (s *SyftScanner) buildGetSourceConfig(src domain.Source, opts ports.ScanOptions) *syft.GetSourceConfig {
	cfg := syft.DefaultGetSourceConfig()

	// Image pull strategy.
	if src.Kind == domain.SourceImage {
		cfg = cfg.WithSources(s.opts.DefaultImagePullSource)
	}

	// Platform constraint.
	if p := effectivePlatform(src, opts); p != nil {
		cfg = cfg.WithPlatform(&stereoscopeimage.Platform{
			Architecture: p.Arch,
			OS:           p.OS,
			Variant:      p.Variant,
		})
	}

	// Registry credentials.
	if src.Credentials != nil {
		cfg = cfg.WithRegistryOptions(buildRegistryOptions(src))
	}

	// Exclusion patterns.
	// v1.42: WithExcludes was removed; use WithExcludeConfig(source.ExcludeConfig).
	if len(opts.ExcludePatterns) > 0 {
		cfg = cfg.WithExcludeConfig(syftSource.ExcludeConfig{
			Paths: opts.ExcludePatterns,
		})
	}

	return cfg
}

// buildCreateSBOMConfig maps ScanOptions to syft's CreateSBOMConfig.
//
// API notes (syft v1.42.x):
//   - Name/tag-based cataloger filtering uses WithCatalogerSelection(cataloging.SelectionRequest).
//     WithCatalogers now accepts concrete cataloger.Cataloger implementations only; it is
//     not the right hook for string-based addition/removal.
//   - SelectionRequest.AddNames appends catalogers by name on top of the default set,
//     equivalent to the former pkgcataloging.NewSelectionRequest().WithAdditions().
//   - Per-request parallelism takes precedence over the scanner-level setting.
func (s *SyftScanner) buildCreateSBOMConfig(opts ports.ScanOptions) *syft.CreateSBOMConfig {
	cfg := syft.DefaultCreateSBOMConfig()

	// Per-request parallelism takes precedence over the scanner-level setting.
	if opts.Parallelism > 0 {
		cfg = cfg.WithParallelism(opts.Parallelism)
	} else if s.opts.Parallelism > 0 {
		cfg = cfg.WithParallelism(s.opts.Parallelism)
	}

	// Cataloger selection by name/tag.
	// v1.42: WithCatalogerSelection replaces the old WithCatalogers(SelectionRequest) pattern.
	if len(opts.Catalogers) > 0 {
		cfg = cfg.WithCatalogerSelection(cataloging.SelectionRequest{
			AddNames: opts.Catalogers,
		})
	}

	return cfg
}

// buildRegistryOptions translates domain.Credentials to stereoscope registry options.
func buildRegistryOptions(src domain.Source) *stereoscopeimage.RegistryOptions {
	creds := src.Credentials
	if creds == nil {
		return nil
	}
	ropts := &stereoscopeimage.RegistryOptions{}
	if creds.Username != "" || creds.Password != "" {
		ropts.Credentials = []stereoscopeimage.RegistryCredentials{
			{
				Authority: registryAuthority(src.Identifier),
				Username:  creds.Username,
				Password:  creds.Password,
				Token:     creds.Token,
			},
		}
	}
	return ropts
}

// registryAuthority extracts the registry host from an image reference.
//
//	"docker.io/library/ubuntu:22.04" → "docker.io"
//	"ghcr.io/org/image:tag"          → "ghcr.io"
func registryAuthority(ref string) string {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) > 1 && strings.ContainsAny(parts[0], ".:") {
		return parts[0]
	}
	return "index.docker.io"
}

// effectivePlatform resolves the platform to use for scanning.
// Per-request opts take precedence over the source-level platform.
func effectivePlatform(src domain.Source, opts ports.ScanOptions) *domain.Platform {
	if opts.Platform != nil {
		return opts.Platform
	}
	return src.Platform
}

// ── type conversion ──────────────────────────────────────────────────────────

// toComponent converts a syft pkg.Package to a domain.Component.
func toComponent(p pkg.Package) domain.Component {
	c := domain.Component{
		ID:         string(p.ID()),
		Name:       p.Name,
		Version:    p.Version,
		Kind:       toComponentKind(p.Type),
		PackageURL: p.PURL,
		Language:   string(p.Language),
	}

	// CPEs.
	// v1.42: cpe.CPE is a struct {Attributes cpe.Attributes; Source cpe.Source}.
	// The canonical CPE 2.3 formatted string is on Attributes.BindToFmtString().
	// cpe.CPE itself has no String() method.
	for _, pkgCPE := range p.CPEs {
		c.CPEs = append(c.CPEs, pkgCPE.Attributes.BindToFmtString())
	}

	// Licenses.
	for _, lic := range p.Licenses.ToSlice() {
		c.Licenses = append(c.Licenses, domain.License{
			ID:         lic.SPDXExpression,
			Name:       lic.Value,
			Expression: lic.SPDXExpression,
			URL:        firstURL(lic.URLs),
		})
	}

	// Locations → domain Locations.
	for _, loc := range p.Locations.ToSlice() {
		c.Locations = append(c.Locations, domain.Location{
			Path:    loc.RealPath,
			LayerID: loc.FileSystemID,
		})
		if c.LayerID == "" {
			c.LayerID = loc.FileSystemID
		}
	}

	return c
}

// toComponentKind maps syft package types to domain component kinds.
func toComponentKind(t pkg.Type) domain.ComponentKind {
	switch t {
	case pkg.ApkPkg, pkg.DebPkg, pkg.RpmPkg, pkg.AlpmPkg, pkg.PortagePkg:
		return domain.KindOS
	case pkg.NpmPkg, pkg.PythonPkg, pkg.GemPkg, pkg.JavaPkg,
		pkg.GoModulePkg, pkg.RustPkg, pkg.DotnetPkg, pkg.PhpComposerPkg:
		return domain.KindLibrary
	default:
		return domain.KindUnknown
	}
}

// toRelationships converts syft artifact.Relationship slices to domain types.
// Relationship types with no domain mapping are silently dropped.
func toRelationships(rels []artifact.Relationship) []domain.Relationship {
	out := make([]domain.Relationship, 0, len(rels))
	for _, r := range rels {
		kind := toRelKind(r.Type)
		if kind == "" {
			continue
		}
		out = append(out, domain.Relationship{
			FromID: string(r.From.ID()),
			ToID:   string(r.To.ID()),
			Kind:   kind,
		})
	}
	return out
}

// toRelKind maps syft artifact.RelationshipType to domain.RelationshipKind.
// Types without a domain analogue return "" and are dropped by toRelationships.
func toRelKind(t artifact.RelationshipType) domain.RelationshipKind {
	switch t {
	case artifact.ContainsRelationship:
		return domain.RelContains
	case artifact.DependencyOfRelationship:
		return domain.RelDependsOn
	default:
		return ""
	}
}

// firstURL returns the first element of a slice, or "" if empty.
func firstURL(urls []string) string {
	if len(urls) > 0 {
		return urls[0]
	}
	return ""
}

// Compile-time interface satisfaction check.
var _ ports.Scanner = (*SyftScanner)(nil)

// Compile-time anchor: keeps the syftsbom import live. The *syftsbom.SBOM
// value travels through domain.SBOM.Raw and is consumed by high-fidelity exporters.
var _ syftsbom.SBOM
