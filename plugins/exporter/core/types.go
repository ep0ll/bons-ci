package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ─── ExporterType ──────────────────────────────────────────────────────────

// ExporterType is the stable, unique string identifier for an exporter.
// New backends register their own ExporterType constants in their own
// packages; this file only declares the well-known built-ins.
type ExporterType string

const (
	ExporterTypeContainerImage ExporterType = "containerimage"
	ExporterTypeOCI            ExporterType = "oci"
	ExporterTypeLocal          ExporterType = "local"
	ExporterTypeTar            ExporterType = "tar"
	// Future backends are declared in their own packages:
	//   ExporterTypeDebian ExporterType = "debian"
	//   ExporterTypeAPT    ExporterType = "apt"
	//   ExporterTypeDallie ExporterType = "dallie"
	//   ExporterTypeHelm   ExporterType = "helm"
)

// ─── Digest ────────────────────────────────────────────────────────────────

// Algorithm is the hash algorithm used to produce a Digest.
type Algorithm string

const (
	AlgorithmSHA256 Algorithm = "sha256"
)

// Digest is a content-addressable identifier: algorithm:hex.
// Modelled after OCI image spec digests; kept as a value type so that
// zero-value checks are unambiguous.
type Digest struct {
	Algorithm Algorithm
	Hex       string
}

// NewDigest constructs a Digest from raw bytes using SHA-256.
func NewDigest(data []byte) Digest {
	h := sha256.Sum256(data)
	return Digest{Algorithm: AlgorithmSHA256, Hex: hex.EncodeToString(h[:])}
}

// ParseDigest parses a "algorithm:hex" string.
func ParseDigest(s string) (Digest, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Digest{}, fmt.Errorf("invalid digest %q: expected algorithm:hex", s)
	}
	return Digest{Algorithm: Algorithm(parts[0]), Hex: parts[1]}, nil
}

// String serialises as "algorithm:hex".
func (d Digest) String() string {
	if d.Hex == "" {
		return ""
	}
	return string(d.Algorithm) + ":" + d.Hex
}

// IsZero reports whether the digest is the zero value.
func (d Digest) IsZero() bool { return d.Hex == "" }

// ─── Platform ──────────────────────────────────────────────────────────────

// Platform describes the target OS/architecture combination.
type Platform struct {
	OS           string
	Architecture string
	Variant      string
	OSVersion    string
}

// String returns a compact human-readable form, e.g. "linux/amd64/v8".
func (p Platform) String() string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Normalize applies canonical form (lower-case, amd64→amd64 etc.).
func (p Platform) Normalize() Platform {
	p.OS = strings.ToLower(p.OS)
	p.Architecture = strings.ToLower(p.Architecture)
	return p
}

// ─── Layer ─────────────────────────────────────────────────────────────────

// MediaType identifies the MIME type of a blob.
type MediaType string

const (
	MediaTypeOCIImageManifest   MediaType = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIImageIndex      MediaType = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIImageConfig     MediaType = "application/vnd.oci.image.config.v1+json"
	MediaTypeOCILayer           MediaType = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaTypeDockerManifest     MediaType = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerManifestList MediaType = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeInTotoStatement    MediaType = "application/vnd.in-toto+json"
)

// BlobDescriptor is a reference to a stored blob.
type BlobDescriptor struct {
	Digest      Digest
	Size        int64
	MediaType   MediaType
	Annotations map[string]string
}

// Layer is a single filesystem layer.
type Layer struct {
	Descriptor BlobDescriptor
	DiffID     Digest // uncompressed digest
	History    *LayerHistory
}

// LayerHistory records how a layer was created (for OCI image config history).
type LayerHistory struct {
	CreatedAt  *time.Time
	CreatedBy  string
	Comment    string
	EmptyLayer bool
}

// ─── Artifact ──────────────────────────────────────────────────────────────

// ArtifactKind distinguishes the primary content type of an Artifact.
type ArtifactKind string

const (
	ArtifactKindContainerImage ArtifactKind = "containerimage"
	ArtifactKindFilesystem     ArtifactKind = "filesystem"
	ArtifactKindPackage        ArtifactKind = "package" // deb, rpm, apk, …
	ArtifactKindAttestation    ArtifactKind = "attestation"
	ArtifactKindGeneric        ArtifactKind = "generic"
)

// Artifact is the immutable, canonical representation of build output
// flowing through the export pipeline. Transformers read and return Artifacts;
// exporters consume them.
//
// Artifact is intentionally kept as a value type: transformers return new
// instances to preserve immutability and make transformations auditable.
type Artifact struct {
	Kind         ArtifactKind
	Platforms    []Platform
	Layers       []Layer
	Config       []byte // serialised image/package config
	Attestations []AttestationRecord
	Annotations  map[string]string
	Labels       map[string]string
	Metadata     map[string][]byte
}

// Clone returns a deep copy safe to mutate without affecting the original.
func (a *Artifact) Clone() *Artifact {
	if a == nil {
		return nil
	}
	clone := *a

	clone.Layers = make([]Layer, len(a.Layers))
	for i, l := range a.Layers {
		if l.History != nil {
			hCopy := *l.History // deep copy the value
			if l.History.CreatedAt != nil {
				tCopy := *l.History.CreatedAt
				hCopy.CreatedAt = &tCopy
			}
			l.History = &hCopy
		}
		clone.Layers[i] = l
	}

	clone.Attestations = make([]AttestationRecord, len(a.Attestations))
	copy(clone.Attestations, a.Attestations)

	clone.Annotations = cloneStringMap(a.Annotations)
	clone.Labels = cloneStringMap(a.Labels)

	clone.Metadata = make(map[string][]byte, len(a.Metadata))
	for k, v := range a.Metadata {
		cp := make([]byte, len(v))
		copy(cp, v)
		clone.Metadata[k] = cp
	}

	if a.Config != nil {
		clone.Config = make([]byte, len(a.Config))
		copy(clone.Config, a.Config)
	}

	return &clone
}

// ─── AttestationRecord ─────────────────────────────────────────────────────

// AttestationKind classifies an attestation payload.
type AttestationKind string

const (
	AttestationKindInToto AttestationKind = "in-toto"
	AttestationKindBundle AttestationKind = "bundle"
	AttestationKindSBOM   AttestationKind = "sbom"
	AttestationKindSLSA   AttestationKind = "slsa"
	AttestationKindVuln   AttestationKind = "vuln"
)

// AttestationRecord carries a single attestation payload and its metadata.
type AttestationRecord struct {
	Kind          AttestationKind
	PredicateType string
	Path          string
	Payload       []byte
	InlineOnly    bool
	Metadata      map[string][]byte
}

// Clone returns a deep copy.
func (r AttestationRecord) Clone() AttestationRecord {
	cp := r
	if r.Payload != nil {
		cp.Payload = make([]byte, len(r.Payload))
		copy(cp.Payload, r.Payload)
	}
	cp.Metadata = make(map[string][]byte, len(r.Metadata))
	for k, v := range r.Metadata {
		b := make([]byte, len(v))
		copy(b, v)
		cp.Metadata[k] = b
	}
	return cp
}

// ─── Options ───────────────────────────────────────────────────────────────

// Options is an untyped key/value map passed through the public API.
// Each exporter's Resolve method parses only the keys it understands and
// surfaces unknown keys as validation errors.
type Options map[string]string

// Get returns the value for key, or "" if absent.
func (o Options) Get(key string) string { return o[key] }

// GetDefault returns the value for key, or def if absent.
func (o Options) GetDefault(key, def string) string {
	if v, ok := o[key]; ok {
		return v
	}
	return def
}

// Clone returns a shallow copy.
func (o Options) Clone() Options {
	cp := make(Options, len(o))
	for k, v := range o {
		cp[k] = v
	}
	return cp
}

// ─── helpers ───────────────────────────────────────────────────────────────

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
