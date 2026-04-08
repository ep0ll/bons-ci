// Package ports defines the outbound port interfaces that infrastructure
// adapters must implement. Nothing in this package imports infrastructure code.
package ports

import (
	"context"
	"io"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
)

// Scanner is the primary port for SBOM extraction.
// Implementations wrap concrete scanning libraries (e.g., Syft, Trivy).
type Scanner interface {
	// Scan inspects the resolved source and returns a populated SBOM.
	// The returned SBOM may include a Raw field set to the scanner-native
	// representation for use by high-fidelity exporters.
	Scan(ctx context.Context, src domain.Source, opts ScanOptions) (*domain.SBOM, error)

	// Name returns a short human-readable identifier used in logs and metrics.
	Name() string

	// Close releases any resources held by the scanner (e.g. temp dirs, open files).
	// Close must be idempotent.
	Close() error
}

// ScanOptions tunes scanner behaviour on a per-request basis.
// All fields are optional; zero values select scanner defaults.
type ScanOptions struct {
	// Catalogers is an explicit allowlist of cataloger IDs to enable.
	// Empty slice means "use the scanner's compiled-in defaults".
	Catalogers []string

	// ExcludePatterns are glob patterns of paths to skip during scanning.
	// Patterns follow the same syntax as .gitignore.
	ExcludePatterns []string

	// ScanLayers, when true, annotates each component with the OCI layer
	// digest that introduced it (containers only).
	ScanLayers bool

	// Platform overrides the target platform for multi-arch image sources.
	// nil means the scanner selects the default or host platform.
	Platform *domain.Platform

	// Parallelism is the maximum number of concurrent cataloger goroutines.
	// 0 means use the scanner default (usually runtime.NumCPU()).
	Parallelism int

	// ExtraParams passes scanner-implementation-specific key/value options
	// (e.g. {"scope": "all-layers"} for Syft).
	ExtraParams map[string]string
}

// Resolver prepares a domain.Source for scanning.
// Resolvers typically handle authentication, image pulling, and path validation.
// Multiple Resolvers may coexist; the engine selects by SourceKind.
type Resolver interface {
	// Resolve validates and enriches the source. For image sources, this may
	// include pulling to the local cache and resolving the image digest.
	// Returns an enriched Source; the original Source is not mutated.
	Resolve(ctx context.Context, src domain.Source) (domain.Source, error)

	// Accepts returns true when this resolver can handle the given source kind.
	// The engine uses this to route incoming requests.
	Accepts(kind domain.SourceKind) bool
}

// Exporter converts an in-memory SBOM into a serialised wire format.
// Exporters that understand the SBOM.Raw field may use native encoding
// for full fidelity (e.g. a SyftExporter using syft's own encoder).
type Exporter interface {
	// Export writes the serialised SBOM to w.
	Export(ctx context.Context, sbom *domain.SBOM, w io.Writer) error

	// Format returns the domain.Format this exporter produces.
	// Used by the engine to route format-selection requests.
	Format() domain.Format
}

// Cache stores and retrieves completed SBOMs keyed by a content-derived digest.
// All methods must be safe for concurrent use.
type Cache interface {
	// Get retrieves a cached SBOM. Returns (nil, nil) on a cache miss.
	// A non-nil error indicates a storage failure (distinct from a miss).
	Get(ctx context.Context, key string) (*domain.SBOM, error)

	// Set stores an SBOM under key. Implementations may enforce a TTL.
	// A non-nil error indicates a storage failure; the scan result is still valid.
	Set(ctx context.Context, key string, sbom *domain.SBOM) error

	// Delete removes a cached entry. A no-op if the key does not exist.
	Delete(ctx context.Context, key string) error
}
