package models

import "time"

type ImageFormat string

const (
	ImageFormatOCI       ImageFormat = "oci"
	ImageFormatDocker    ImageFormat = "docker"
	ImageFormatNydus     ImageFormat = "nydus"
	ImageFormatEStargz   ImageFormat = "estargz"
	ImageFormatZstd      ImageFormat = "zstd"
	ImageFormatOverlayBD ImageFormat = "overlaybd"
)

// BlobDescriptor describes a single blob in the registry.
type BlobDescriptor struct {
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	// Role: "layer", "config", "manifest", "index"
	Role        string            `json:"role,omitempty"`
	Format      ImageFormat       `json:"format,omitempty"`
	Repository  string            `json:"repository,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// ManifestDescriptor describes a manifest (image or index).
type ManifestDescriptor struct {
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Size        int64             `json:"size"`
	Repository  string            `json:"repository"`
	Tag         string            `json:"tag,omitempty"`
	Blobs       []BlobDescriptor  `json:"blobs,omitempty"` // layers
	Config      *BlobDescriptor   `json:"config,omitempty"`
	Format      ImageFormat       `json:"format"`
	Platform    *Platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Subject     *BlobDescriptor   `json:"subject,omitempty"`   // OCI 1.1 referrers
	ArtifactType string           `json:"artifactType,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// UploadSession tracks an in-progress blob upload.
type UploadSession struct {
	UUID        string            `json:"uuid"`
	Repository  string            `json:"repository"`
	Offset      int64             `json:"offset"`
	Digest      string            `json:"digest,omitempty"` // set on completion
	StorageKey  string            `json:"storage_key"`      // internal staging key
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

// ConversionRecord tracks an image format conversion.
type ConversionRecord struct {
	ID           string           `json:"id"`
	SourceDigest string           `json:"source_digest"`
	SourceRepo   string           `json:"source_repo"`
	SourceFormat ImageFormat      `json:"source_format"`
	SourceBlobs  []BlobDescriptor `json:"source_blobs"`
	TargetDigest string           `json:"target_digest"`
	TargetRepo   string           `json:"target_repo"`
	TargetFormat ImageFormat      `json:"target_format"`
	TargetBlobs  []BlobDescriptor `json:"target_blobs"`
	// Diff sets for quick existence checking
	SourceOnlyDigests []string          `json:"source_only_digests,omitempty"`
	TargetOnlyDigests []string          `json:"target_only_digests,omitempty"`
	SharedDigests     []string          `json:"shared_digests,omitempty"`
	ConvertedAt       time.Time         `json:"converted_at"`
	ConvertedBy       string            `json:"converted_by,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}
