// Package types defines all core SLSA data structures, constants, and enums.
// It has zero external dependencies and is safe to import anywhere.
//
// SLSA levels:
//
//	Level 1 – Provenance exists (may be unsigned).
//	Level 2 – Hosted build platform; signed provenance.
//	Level 3 – Hardened builds: isolated, parameterised, non-falsifiable provenance.
//	Level 4 – Hermetic, reproducible, two-party-reviewed (SLSA v0.x definition).
package types

import "time"

// ─── SLSA version ────────────────────────────────────────────────────────────

// SLSAVersion identifies which revision of the SLSA specification applies.
type SLSAVersion string

const (
	SLSAVersion1  SLSAVersion = "v1"
	SLSAVersion02 SLSAVersion = "v0.2"
)

// ─── SLSA level ───────────────────────────────────────────────────────────────

// Level is a SLSA compliance level (0 = none, 1–4).
type Level int

const (
	LevelNone Level = 0
	Level1    Level = 1
	Level2    Level = 2
	Level3    Level = 3
	// Level4 is the highest level defined in SLSA v0.x. In SLSA v1.0 it is
	// absorbed into a strengthened Level 3; we keep the constant for libraries
	// that still need to distinguish the v0.x notion.
	Level4 Level = 4
)

// String returns the SLSA BUILD level name.
func (l Level) String() string {
	switch l {
	case LevelNone:
		return "none"
	case Level1:
		return "SLSA_BUILD_LEVEL_1"
	case Level2:
		return "SLSA_BUILD_LEVEL_2"
	case Level3:
		return "SLSA_BUILD_LEVEL_3"
	case Level4:
		return "SLSA_BUILD_LEVEL_4"
	default:
		return "unknown"
	}
}

// ─── Build-type URIs ─────────────────────────────────────────────────────────

const (
	BuildTypeBuildKitV1  = "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md"
	BuildTypeBuildKitV02 = "https://mobyproject.org/buildkit@v1"
	BuildTypeGenericV1   = "https://slsa.dev/generic/v1"
	BuildTypeDockerfile  = "https://docs.docker.com/build/slsa"
)

// ─── Predicate-type URIs ─────────────────────────────────────────────────────

const (
	PredicateSLSAProvenanceV1  = "https://slsa.dev/provenance/v1"
	PredicateSLSAProvenanceV02 = "https://slsa.dev/provenance/v0.2"
	PredicateSPDX              = "https://spdx.dev/Document"
)

// PayloadTypeInToto is the DSSE media type for in-toto statements.
const PayloadTypeInToto = "application/vnd.in-toto+json"

// InTotoStatementTypeV01 is the _type field value for in-toto v0.1 statements.
const InTotoStatementTypeV01 = "https://in-toto.io/Statement/v0.1"

// ─── Source types ─────────────────────────────────────────────────────────────

// Platform identifies an OS/architecture pair.
type Platform struct {
	OS           string `json:"os,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	Variant      string `json:"variant,omitempty"`
	OSVersion    string `json:"osVersion,omitempty"`
}

// ImageSource is a container image consumed as a build input.
type ImageSource struct {
	Ref      string    `json:"ref"`
	Platform *Platform `json:"platform,omitempty"`
	Digest   string    `json:"digest,omitempty"` // "sha256:abc…"
	Local    bool      `json:"local,omitempty"`
}

// ImageBlobSource is a raw image blob (manifest, config layer…).
type ImageBlobSource struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest,omitempty"`
	Local  bool   `json:"local,omitempty"`
}

// GitSource is a Git repository consumed as a build context.
type GitSource struct {
	URL    string `json:"url"`
	Commit string `json:"commit,omitempty"`
	Ref    string `json:"ref,omitempty"` // e.g. "refs/heads/main"
}

// HTTPSource is a file downloaded over HTTP/HTTPS.
type HTTPSource struct {
	URL    string `json:"url"`
	Digest string `json:"digest,omitempty"`
}

// LocalSource is a named local directory used as a build context.
type LocalSource struct {
	Name string `json:"name"`
}

// Sources groups every kind of build input.
type Sources struct {
	Images     []ImageSource
	ImageBlobs []ImageBlobSource
	Git        []GitSource
	HTTP       []HTTPSource
	Local      []LocalSource
}

// ─── Secret / SSH ─────────────────────────────────────────────────────────────

// Secret is a secret mounted during the build.
type Secret struct {
	ID       string `json:"id"`
	Optional bool   `json:"optional,omitempty"`
}

// SSH is an SSH agent socket forwarded during the build.
type SSH struct {
	ID       string `json:"id"`
	Optional bool   `json:"optional,omitempty"`
}

// ─── Build configuration ──────────────────────────────────────────────────────

// BuildStep is a single instruction in the resolved build graph.
type BuildStep struct {
	ID     string   `json:"id,omitempty"`
	Inputs []string `json:"inputs,omitempty"`
	// Op is kept as any so callers can embed platform-specific op types.
	Op any `json:"op,omitempty"`
}

// BuildConfig holds the fully-resolved build graph embedded in provenance.
type BuildConfig struct {
	Definition    []BuildStep       `json:"llbDefinition,omitempty"`
	DigestMapping map[string]string `json:"digestMapping,omitempty"`
}

// ─── Predicate building blocks ────────────────────────────────────────────────

// Subject is a build artifact whose provenance is being attested.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// Material is a resolved dependency of the build.
type Material struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest,omitempty"`
}

// BuildParameters holds all user-visible build inputs.
type BuildParameters struct {
	Frontend string            `json:"frontend,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
	Secrets  []*Secret         `json:"secrets,omitempty"`
	SSH      []*SSH            `json:"ssh,omitempty"`
	Locals   []*LocalSource    `json:"locals,omitempty"`
}

// BuilderInfo describes the entity that produced the build.
type BuilderInfo struct {
	ID      string            `json:"id"`
	Version map[string]string `json:"version,omitempty"`
}

// BuildMetadata records timing and identification for a build invocation.
type BuildMetadata struct {
	InvocationID string     `json:"invocationID,omitempty"`
	StartedOn    *time.Time `json:"startedOn,omitempty"`
	FinishedOn   *time.Time `json:"finishedOn,omitempty"`
}

// Completeness records which provenance fields are guaranteed to be accurate.
type Completeness struct {
	Parameters  bool `json:"parameters"`
	Environment bool `json:"environment"`
	Materials   bool `json:"materials"`
}

// ─── Level requirements ───────────────────────────────────────────────────────

// LevelRequirements lists the boolean flags mandated by a SLSA level.
type LevelRequirements struct {
	Signed               bool
	NonFalsifiable       bool
	Isolated             bool
	DependenciesComplete bool
	Hermetic             bool
	Reproducible         bool
	TwoPartyReview       bool
}

// RequirementsFor returns the LevelRequirements for l.
func RequirementsFor(l Level) LevelRequirements {
	switch l {
	case Level1:
		return LevelRequirements{}
	case Level2:
		return LevelRequirements{Signed: true}
	case Level3:
		return LevelRequirements{Signed: true, NonFalsifiable: true, Isolated: true}
	case Level4:
		return LevelRequirements{
			Signed: true, NonFalsifiable: true, Isolated: true,
			DependenciesComplete: true, Hermetic: true,
			Reproducible: true, TwoPartyReview: true,
		}
	default:
		return LevelRequirements{}
	}
}
