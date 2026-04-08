// Package domain contains the pure business entities for SBOM generation.
// No infrastructure dependencies are permitted here.
package domain

import "time"

// Format is the wire encoding of a serialised SBOM.
type Format string

const (
	FormatCycloneDXJSON Format = "cyclonedx+json"
	FormatCycloneDXXML  Format = "cyclonedx+xml"
	FormatSPDXJSON      Format = "spdx+json"
	FormatSPDXTagValue  Format = "spdx+tag-value"
	FormatSyftJSON      Format = "syft+json"
)

// KnownFormats lists all formats supported by the engine.
var KnownFormats = []Format{
	FormatCycloneDXJSON,
	FormatCycloneDXXML,
	FormatSPDXJSON,
	FormatSPDXTagValue,
	FormatSyftJSON,
}

// IsKnownFormat returns true if f is in KnownFormats.
func IsKnownFormat(f Format) bool {
	for _, kf := range KnownFormats {
		if kf == f {
			return true
		}
	}
	return false
}

// SBOM is the root aggregate: a complete Software Bill of Materials.
type SBOM struct {
	// ID is a stable, opaque identifier for this SBOM instance.
	ID string
	// Source describes the scanned artifact.
	Source Source
	// Components are the discovered software artifacts.
	Components []Component
	// Relationships encodes directed edges between components.
	Relationships []Relationship
	// Metadata carries provenance information.
	Metadata Metadata
	// Format records which encoding was requested.
	Format Format
	// GeneratedAt is set by the scanner when the scan completes.
	GeneratedAt time.Time
	// Raw holds the scanner-native SBOM (e.g. *syft_sbom.SBOM).
	// Exporters may use this for high-fidelity serialisation.
	// It must be treated as opaque by all non-exporter code.
	Raw any
}

// Component is an individual software artifact discovered during scanning.
type Component struct {
	// ID is a unique, stable identifier within this SBOM (e.g. pkg fingerprint).
	ID string
	// Name is the human-readable package name.
	Name string
	// Version is the package version string.
	Version string
	// Kind classifies what type of artifact this is.
	Kind ComponentKind
	// PackageURL is the PURL for this component (https://github.com/package-url/purl-spec).
	PackageURL string
	// CPEs are Common Platform Enumeration identifiers for this component.
	CPEs []string
	// Licenses are the declared licenses for this component.
	Licenses []License
	// Checksums maps algorithm name → hex digest (e.g. "sha256" → "abc123...").
	Checksums map[string]string
	// Locations records where in the source this component was found.
	Locations []Location
	// LayerID is the OCI layer digest that introduced this component, if applicable.
	LayerID string
	// Language is the programming ecosystem (e.g. "python", "java", "go").
	Language string
}

// ComponentKind classifies a component.
type ComponentKind string

const (
	KindLibrary     ComponentKind = "library"
	KindApplication ComponentKind = "application"
	KindFramework   ComponentKind = "framework"
	KindOS          ComponentKind = "os"
	KindContainer   ComponentKind = "container"
	KindFile        ComponentKind = "file"
	KindUnknown     ComponentKind = "unknown"
)

// License represents an SPDX or custom license declaration.
type License struct {
	// ID is the SPDX identifier, e.g. "MIT", "Apache-2.0".
	ID string
	// Name is the human-readable license name when no SPDX ID is available.
	Name string
	// Expression is a full SPDX expression for composite licenses.
	Expression string
	// URL links to the full license text.
	URL string
}

// Location records a single occurrence of a component within the source.
type Location struct {
	// Path is the virtual path within the source (e.g. "/usr/lib/python3/site-packages/...").
	Path string
	// LayerID is the OCI layer digest containing this file.
	LayerID string
	// FileMode is the Unix permission bits.
	FileMode uint32
}

// Relationship encodes a directed edge between two components.
type Relationship struct {
	// FromID is the component that depends on or contains ToID.
	FromID string
	// ToID is the component being referenced.
	ToID string
	// Kind classifies the relationship.
	Kind RelationshipKind
}

// RelationshipKind classifies how two components relate.
type RelationshipKind string

const (
	RelDependsOn    RelationshipKind = "depends-on"
	RelDevDependsOn RelationshipKind = "dev-depends-on"
	RelContains     RelationshipKind = "contains"
	RelPackages     RelationshipKind = "packages"
	RelBuiltFrom    RelationshipKind = "built-from"
)

// Metadata carries provenance and tooling information about how an SBOM was produced.
type Metadata struct {
	// Tool describes the generator.
	Tool Tool
	// Lifecycle records the build stage at which the SBOM was generated.
	Lifecycle LifecyclePhase
	// Supplier is the entity providing the software.
	Supplier string
	// Authors lists the SBOM document authors.
	Authors []string
	// Tags are arbitrary key/value annotations.
	Tags map[string]string
}

// Tool describes the program that produced the SBOM.
type Tool struct {
	Vendor  string
	Name    string
	Version string
}

// LifecyclePhase records when in the software lifecycle the SBOM was generated.
type LifecyclePhase string

const (
	LifecycleBuild   LifecyclePhase = "build"
	LifecycleRelease LifecyclePhase = "release"
	LifecycleDeploy  LifecyclePhase = "deploy"
	LifecycleRuntime LifecyclePhase = "runtime"
)
