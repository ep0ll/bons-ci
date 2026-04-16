// Package types defines all core interfaces, types, and constants for
// the AccelRegistry — an OCI-compatible registry for accelerated container images.
//
// Design principles:
//   - Interface segregation: small, focused interfaces
//   - Every public type is documented
//   - Digest-centric: all linkage is via content-addressed digests
//   - Extensible: new accel types register via AccelHandler
package types

import (
	"context"
	"io"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ────────────────────────────────────────────────────────────────────────────
// Acceleration Types
// ────────────────────────────────────────────────────────────────────────────

// AccelType identifies a specific image-layer acceleration technology.
type AccelType string

const (
	AccelNydus     AccelType = "nydus"
	AccelEstargz   AccelType = "estargz"
	AccelSOCI      AccelType = "soci"
	AccelOverlayBD AccelType = "overlaybd"
	AccelUnknown   AccelType = "unknown"
)

// KnownAccelTypes lists all officially supported acceleration types.
var KnownAccelTypes = []AccelType{
	AccelNydus, AccelEstargz, AccelSOCI, AccelOverlayBD,
}

// ────────────────────────────────────────────────────────────────────────────
// Annotation Keys — well-known annotation namespaces
// ────────────────────────────────────────────────────────────────────────────

const (
	// AnnotationSourceDigest is the canonical annotation for the source
	// (non-accelerated) image manifest digest that an accel image was
	// derived from. Present on accel manifests and their configs.
	AnnotationSourceDigest = "org.accelregistry.source.digest"

	// AnnotationSourceIndexDigest is the source image *index* digest
	// (multi-arch). May differ from the single-arch manifest digest.
	AnnotationSourceIndexDigest = "org.accelregistry.source.index.digest"

	// AnnotationAccelType identifies the acceleration type.
	AnnotationAccelType = "org.accelregistry.accel.type"

	// AnnotationAccelVersion is the version of the accel toolchain used.
	AnnotationAccelVersion = "org.accelregistry.accel.version"

	// AnnotationVisibility marks whether the image is "public" or "private".
	AnnotationVisibility = "org.accelregistry.visibility"

	// ── Nydus community annotations ──────────────────────────────────────────
	NydusAnnotationSourceDigest  = "containerd.io/snapshot/nydus-source-digest"
	NydusAnnotationVersion       = "containerd.io/snapshot/nydus-version"
	NydusLayerMediaType          = "application/vnd.oci.image.layer.nydus.blob.v1"
	NydusBootstrapMediaType      = "application/vnd.oci.image.layer.nydus.bootstrap.v1"

	// ── eStargz / Stargz-Snapshotter annotations ─────────────────────────────
	StargzAnnotationTOCDigest    = "containerd.io/snapshot/stargz/toc.digest"
	StargzAnnotationUncompressed = "containerd.io/uncompressed"

	// ── SOCI (Seekable OCI) annotations ──────────────────────────────────────
	SOCIArtifactType             = "application/vnd.amazon.soci.index.v1+json"
	SOCILayerMediaType           = "application/octet-stream"
	SOCIAnnotationIndexDigest    = "com.amazon.soci.index-digest"

	// ── OverlayBD annotations ─────────────────────────────────────────────────
	OverlayBDAnnotationVersion   = "containerd.io/snapshot/overlaybd/version"
	OverlayBDAnnotationLayer     = "containerd.io/snapshot/overlaybd.layer"
	OverlayBDLayerMediaType      = "application/vnd.containerd.overlaybd.v1+gz"
)

// ────────────────────────────────────────────────────────────────────────────
// Source Reference — a pointer back to a non-accelerated artefact
// ────────────────────────────────────────────────────────────────────────────

// SourceRefKind classifies which OCI artefact a SourceRef points at.
type SourceRefKind string

const (
	SourceRefIndex    SourceRefKind = "index"    // OCI Image Index
	SourceRefManifest SourceRefKind = "manifest" // OCI Image Manifest
	SourceRefConfig   SourceRefKind = "config"   // Image config blob
	SourceRefLayer    SourceRefKind = "layer"    // Individual layer blob
)

// SourceRef is a structured pointer from an accel artefact back to the
// original content it was derived from. Every entry stored in the registry
// carries at least one SourceRef, enabling reverse lookup.
type SourceRef struct {
	Digest      digest.Digest     `json:"digest"`
	MediaType   string            `json:"mediaType,omitempty"`
	Kind        SourceRefKind     `json:"kind"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// Visibility
// ────────────────────────────────────────────────────────────────────────────

// Visibility describes the access control state of an image.
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
	VisibilityUnknown Visibility = "unknown"
)

// ────────────────────────────────────────────────────────────────────────────
// AccelVariant — a single accelerated image entry
// ────────────────────────────────────────────────────────────────────────────

// AccelVariant records a single accelerated image together with every digest
// that links it back to its non-accelerated source(s).
type AccelVariant struct {
	// AccelType is the acceleration technology.
	AccelType AccelType `json:"accelType"`

	// ManifestDigest is the OCI manifest digest of this accel image.
	ManifestDigest digest.Digest `json:"manifestDigest"`

	// IndexDigest is the optional multi-arch index digest (if any).
	IndexDigest digest.Digest `json:"indexDigest,omitempty"`

	// Repository is the OCI repository name (e.g. "library/node").
	Repository string `json:"repository"`

	// Tag is the human-readable tag, if present.
	Tag string `json:"tag,omitempty"`

	// Platform identifies the OS/arch this variant targets.
	Platform *ocispec.Platform `json:"platform,omitempty"`

	// Annotations carried on the accel manifest.
	Annotations map[string]string `json:"annotations"`

	// Size is the total compressed size of all layers in bytes.
	Size int64 `json:"size"`

	// CreatedAt is when this variant was indexed.
	CreatedAt time.Time `json:"createdAt"`

	// Visibility of this image.
	Visibility Visibility `json:"visibility"`

	// SourceRefs is the complete set of source digests this variant was
	// derived from, covering all artefact types (index, manifest, config,
	// individual layers). This is the core linking mechanism.
	SourceRefs []SourceRef `json:"sourceRefs"`
}

// ────────────────────────────────────────────────────────────────────────────
// Attestation — SBOM / signature artefacts
// ────────────────────────────────────────────────────────────────────────────

// Attestation holds metadata for an OCI referrer artefact (SBOM, cosign
// signature, SLSA provenance, etc.) associated with an image.
type Attestation struct {
	Type        string            `json:"type"`
	Digest      digest.Digest     `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Annotations map[string]string `json:"annotations"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// ────────────────────────────────────────────────────────────────────────────
// Query & Result types
// ────────────────────────────────────────────────────────────────────────────

// AccelQueryResult is returned by QueryAccel and describes all acceleration
// variants available for a given source digest.
type AccelQueryResult struct {
	// SourceDigest is the queried non-accelerated image digest.
	SourceDigest digest.Digest `json:"sourceDigest"`

	// Found is true when at least one accel variant exists.
	Found bool `json:"found"`

	// TotalVariants is the sum of all variants across all accel types.
	TotalVariants int `json:"totalVariants"`

	// Variants groups all AccelVariants by their AccelType.
	Variants map[AccelType][]AccelVariant `json:"variants"`

	// SupportedTypes is the list of AccelTypes for which at least one
	// variant was found.
	SupportedTypes []AccelType `json:"supportedTypes"`

	// Attestations lists all referrer artefacts for the source digest.
	Attestations []Attestation `json:"attestations,omitempty"`

	// Referrers is the raw OCI 1.1 referrers list for the source digest.
	Referrers []ocispec.Descriptor `json:"referrers,omitempty"`
}

// PullRequest selects which accel types to pull, and optionally a platform.
type PullRequest struct {
	// SourceDigest is the non-accelerated image to resolve.
	SourceDigest digest.Digest `json:"sourceDigest"`

	// AccelTypes filters the pull to specific types. Empty = all types.
	AccelTypes []AccelType `json:"accelTypes,omitempty"`

	// Platform filters to a specific platform. Nil = all platforms.
	Platform *ocispec.Platform `json:"platform,omitempty"`
}

// PullResult is returned by a pull operation.
type PullResult struct {
	SourceDigest digest.Digest            `json:"sourceDigest"`
	Pulled       []AccelVariant           `json:"pulled"`
	Errors       map[AccelType]string     `json:"errors,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// DAG — content-addressed directed acyclic graph
// ────────────────────────────────────────────────────────────────────────────

// DAGNode is a single vertex in an OCI content DAG (image index → manifest
// → config + layers).
type DAGNode struct {
	// Digest is the content digest of this node.
	Digest digest.Digest `json:"digest"`

	// MediaType is the OCI media type.
	MediaType string `json:"mediaType"`

	// Size is the blob size in bytes.
	Size int64 `json:"size"`

	// Exists is true if this blob is present in the accel registry.
	Exists bool `json:"exists"`

	// Children are the nodes this node directly references.
	Children []*DAGNode `json:"children,omitempty"`

	// Depth is the node's depth from the root (root = 0).
	Depth int `json:"depth"`

	// Metadata is free-form additional information (accel type, etc.).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// DAGQueryResult describes the result of traversing an OCI content DAG.
type DAGQueryResult struct {
	// RootDigest is the digest the traversal was rooted at.
	RootDigest digest.Digest `json:"rootDigest"`

	// TotalNodes is the count of unique nodes discovered.
	TotalNodes int `json:"totalNodes"`

	// ExistingNodes is the count of nodes whose blobs exist in this registry.
	ExistingNodes int `json:"existingNodes"`

	// MissingNodes is the count of nodes whose blobs are absent.
	MissingNodes int `json:"missingNodes"`

	// Root is the root DAGNode with its full children tree.
	Root *DAGNode `json:"root"`

	// IsComplete is true when every node in the DAG exists.
	IsComplete bool `json:"isComplete"`

	// AccelTypes lists any accel types detected in the DAG.
	AccelTypes []AccelType `json:"accelTypes,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// Image Metadata
// ────────────────────────────────────────────────────────────────────────────

// ImageMetadata holds rich metadata about an image stored in the registry.
type ImageMetadata struct {
	Digest       digest.Digest     `json:"digest"`
	Repository   string            `json:"repository"`
	Tags         []string          `json:"tags"`
	Visibility   Visibility        `json:"visibility"`
	IsAccel      bool              `json:"isAccel"`
	AccelType    AccelType         `json:"accelType,omitempty"`
	SourceDigest digest.Digest     `json:"sourceDigest,omitempty"`
	TotalSize    int64             `json:"totalSize"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	Platform     *ocispec.Platform `json:"platform,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// Upload Session
// ────────────────────────────────────────────────────────────────────────────

// UploadSession represents an in-progress blob upload.
type UploadSession struct {
	UUID      string    `json:"uuid"`
	Repo      string    `json:"repo"`
	StartedAt time.Time `json:"startedAt"`
	BytesRead int64     `json:"bytesRead"`
	TmpPath   string    `json:"-"`
}

// ────────────────────────────────────────────────────────────────────────────
// Interfaces
// ────────────────────────────────────────────────────────────────────────────

// AccelDetector examines an OCI manifest + config blob and reports
// whether it is an accelerated image and of which type.
//
// Each AccelType implements this interface and is registered via
// accel.Registry.Register().
type AccelDetector interface {
	// Detect returns the AccelType and true if the manifest is accelerated.
	Detect(ctx context.Context, manifest ocispec.Manifest, configBlob []byte) (AccelType, bool, error)

	// Name returns this detector's AccelType.
	Name() AccelType
}

// AccelHandler extends AccelDetector with the ability to extract all
// SourceRefs from an accelerated image — the set of original content
// digests that the accel image was derived from.
type AccelHandler interface {
	AccelDetector

	// ExtractSourceRefs returns every SourceRef that links this accel
	// manifest back to its non-accelerated origin. Must cover index,
	// manifest, config, and per-layer digests where available.
	ExtractSourceRefs(ctx context.Context, manifest ocispec.Manifest, configBlob []byte) ([]SourceRef, error)

	// Validate verifies the structural integrity of an accel manifest.
	Validate(ctx context.Context, manifest ocispec.Manifest) error
}

// ContentStore is the low-level content-addressed blob store.
// It must be safe for concurrent use.
type ContentStore interface {
	// Get opens a read stream for the given digest.
	Get(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error)

	// Put writes r as a new blob. Implementations must verify the digest.
	Put(ctx context.Context, dgst digest.Digest, r io.Reader, size int64) error

	// Exists returns true without reading data if the blob is present.
	Exists(ctx context.Context, dgst digest.Digest) (bool, error)

	// Delete removes a blob from the store.
	Delete(ctx context.Context, dgst digest.Digest) error

	// Info returns size and metadata for a blob.
	Info(ctx context.Context, dgst digest.Digest) (ContentInfo, error)

	// Walk iterates over all blobs. The callback must not call Walk again.
	Walk(ctx context.Context, fn func(ContentInfo) error) error
}

// ContentInfo contains metadata about a stored blob.
type ContentInfo struct {
	Digest    digest.Digest
	Size      int64
	CreatedAt time.Time
	Labels    map[string]string
}

// AccelIndex maps non-accelerated source digests to their AccelVariants.
// Implementations MUST be safe for concurrent use.
type AccelIndex interface {
	// Index records a new accel variant. Idempotent by ManifestDigest.
	Index(ctx context.Context, variant AccelVariant) error

	// Query returns all accel variants for the given source digest.
	// Returns a result with Found=false (not an error) when absent.
	Query(ctx context.Context, sourceDigest digest.Digest) (*AccelQueryResult, error)

	// ExistsAny is a fast (bloom-filter-backed) existence check.
	// May return false-positives; never returns false-negatives.
	ExistsAny(ctx context.Context, sourceDigest digest.Digest) bool

	// ExistsByType is a precise per-type existence check.
	ExistsByType(ctx context.Context, sourceDigest digest.Digest, t AccelType) bool

	// Remove deletes all variants for a source digest.
	Remove(ctx context.Context, sourceDigest digest.Digest) error

	// RemoveVariant removes a single accel variant by its manifest digest.
	RemoveVariant(ctx context.Context, sourceDigest digest.Digest, manifestDigest digest.Digest) error

	// Stats returns aggregate statistics about the index.
	Stats() IndexStats
}

// IndexStats describes the state of an AccelIndex.
type IndexStats struct {
	TotalSourceDigests     int64            `json:"totalSourceDigests"`
	TotalVariants          map[AccelType]int64 `json:"totalVariants"`
	BloomFalsePositiveRate float64          `json:"bloomFalsePositiveRate"`
	ShardCount             int              `json:"shardCount"`
}

// Registry is the central OCI + Accel registry interface.
type Registry interface {
	// ── OCI Distribution Spec (v2) ──────────────────────────────────────────

	GetManifest(ctx context.Context, repo, ref string) (ocispec.Descriptor, []byte, error)
	PutManifest(ctx context.Context, repo, ref, mediaType string, manifest []byte) (digest.Digest, error)
	DeleteManifest(ctx context.Context, repo string, dgst digest.Digest) error
	GetBlob(ctx context.Context, repo string, dgst digest.Digest) (io.ReadCloser, int64, error)
	InitiateUpload(ctx context.Context, repo string) (string, error)
	ChunkUpload(ctx context.Context, repo, uuid string, r io.Reader, start, end int64) error
	FinalizeUpload(ctx context.Context, repo, uuid string, dgst digest.Digest) error
	GetTags(ctx context.Context, repo, last string, n int) ([]string, error)

	// ── OCI 1.1 Referrers API ───────────────────────────────────────────────

	GetReferrers(ctx context.Context, repo string, dgst digest.Digest, artifactType string) ([]ocispec.Descriptor, error)

	// ── Accel-specific ──────────────────────────────────────────────────────

	// QueryAccel returns all accel variants for a non-accelerated source digest.
	QueryAccel(ctx context.Context, sourceDigest digest.Digest) (*AccelQueryResult, error)

	// PullAccel resolves a PullRequest to a set of AccelVariants.
	PullAccel(ctx context.Context, req PullRequest) (*PullResult, error)

	// GetDAG traverses the OCI content DAG rooted at dgst and reports which
	// nodes exist in this registry.
	GetDAG(ctx context.Context, repo string, dgst digest.Digest) (*DAGQueryResult, error)

	// GetImageMetadata returns rich metadata for an image.
	GetImageMetadata(ctx context.Context, repo string, dgst digest.Digest) (*ImageMetadata, error)
}

// MetadataStore persists and queries image metadata.
// Implementations MUST be safe for concurrent use.
type MetadataStore interface {
	Put(ctx context.Context, meta ImageMetadata) error
	Get(ctx context.Context, repo string, dgst digest.Digest) (*ImageMetadata, error)
	Delete(ctx context.Context, repo string, dgst digest.Digest) error
	ListByAccelType(ctx context.Context, t AccelType) ([]ImageMetadata, error)
	ListBySourceDigest(ctx context.Context, sourceDigest digest.Digest) ([]ImageMetadata, error)
	ListByRepo(ctx context.Context, repo string) ([]ImageMetadata, error)
}

// ReferrersStore maintains the OCI 1.1 referrers index.
type ReferrersStore interface {
	// AddReferrer records that manifestDesc has subject subjectDigest.
	AddReferrer(ctx context.Context, repo string, subjectDigest digest.Digest, manifestDesc ocispec.Descriptor) error
	// GetReferrers returns all descriptors whose subject is subjectDigest.
	GetReferrers(ctx context.Context, repo string, subjectDigest digest.Digest, artifactType string) ([]ocispec.Descriptor, error)
	// RemoveReferrer removes a referrer mapping.
	RemoveReferrer(ctx context.Context, repo string, subjectDigest digest.Digest, manifestDigest digest.Digest) error
}

// DAGTraverser traverses the OCI content DAG for a given root descriptor.
type DAGTraverser interface {
	Traverse(ctx context.Context, repo string, root ocispec.Descriptor, store ContentStore) (*DAGQueryResult, error)
}

// AuditLogger records registry events for auditability.
type AuditLogger interface {
	LogPush(ctx context.Context, repo string, desc ocispec.Descriptor, variant *AccelVariant)
	LogPull(ctx context.Context, repo string, desc ocispec.Descriptor)
	LogQuery(ctx context.Context, sourceDigest digest.Digest, result *AccelQueryResult)
	LogDelete(ctx context.Context, repo string, dgst digest.Digest)
}

// Metrics records Prometheus-style metrics.
type Metrics interface {
	IncPush(accelType AccelType)
	IncPull(accelType AccelType)
	IncQuery(found bool)
	ObserveIndexLatency(op string, d time.Duration)
	ObserveStorageLatency(op string, d time.Duration)
	SetIndexSize(accelType AccelType, count int64)
}
