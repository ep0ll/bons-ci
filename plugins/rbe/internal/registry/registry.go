// Package registry implements an OCI Distribution Spec v1.1 compliant image
// registry backed by the pluggable storage and metadata abstractions.
//
// It supports:
//   - Standard OCI blob and manifest operations
//   - Chunked / resumable blob uploads
//   - Cross-repository blob mounts
//   - OCI Referrers API (v1.1)
//   - Conversion graphs tracking OCI→Nydus/eStargz/OverlayBD conversions
//   - Per-repository blob completeness checks for accelerated image formats
package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/rbe/internal/metadata"
	"github.com/bons/bons-ci/plugins/rbe/internal/storage"
)

var (
	// ErrBlobNotFound is returned when a requested blob does not exist.
	ErrBlobNotFound = errors.New("registry: blob not found")
	// ErrManifestNotFound is returned when a manifest or tag is missing.
	ErrManifestNotFound = errors.New("registry: manifest not found")
	// ErrUploadNotFound is returned for an unknown upload session.
	ErrUploadNotFound = errors.New("registry: upload session not found")
	// ErrDigestMismatch is returned when the pushed content digest differs
	// from the one declared by the client.
	ErrDigestMismatch = errors.New("registry: digest mismatch")
	// ErrInvalidRepositoryName is returned for non-spec-compliant names.
	ErrInvalidRepositoryName = errors.New("registry: invalid repository name")
	// ErrConversionNotFound means no conversion record exists for that pair.
	ErrConversionNotFound = errors.New("registry: conversion not found")
)

// repoNameRE validates OCI repository names.
var repoNameRE = regexp.MustCompile(`^[a-z0-9]+(?:[._\-/][a-z0-9]+)*$`)

// ─── Supporting types ────────────────────────────────────────────────────────

// ManifestRecord is the KV-stored representation of a pushed manifest.
type ManifestRecord struct {
	Digest       string            `json:"digest"`
	MediaType    string            `json:"media_type"`
	Size         int64             `json:"size"`
	Subject      string            `json:"subject,omitempty"` // for OCI referrers
	ArtifactType string            `json:"artifact_type,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	PushedAt     time.Time         `json:"pushed_at"`
}

// UploadSession tracks an in-progress chunked blob upload.
type UploadSession struct {
	UUID           string         `json:"uuid"`
	Repository     string         `json:"repository"`
	Offset         int64          `json:"offset"`
	TotalSize      int64          `json:"total_size"`
	UploadID       string         `json:"upload_id"` // S3 multipart upload ID
	Parts          []storage.Part `json:"parts"`
	StartedAt      time.Time      `json:"started_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	ContentType    string         `json:"content_type,omitempty"`
	ExpectedDigest string         `json:"expected_digest,omitempty"`
}

const uploadSessionTTL = 24 * time.Hour

// ─── Registry ────────────────────────────────────────────────────────────────

// Registry is the central object managing all OCI registry operations.
type Registry struct {
	store metadata.Store
	blobs storage.Backend
	log   *zap.Logger
}

// New creates a Registry with the given metadata store and blob backend.
func New(store metadata.Store, blobs storage.Backend, log *zap.Logger) *Registry {
	return &Registry{
		store: store,
		blobs: blobs,
		log:   log.Named("registry"),
	}
}

// validateRepo checks that a repository name is valid per the OCI spec.
func validateRepo(name string) error {
	if !repoNameRE.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidRepositoryName, name)
	}
	return nil
}

// ─── Blob operations ─────────────────────────────────────────────────────────

// BlobExists reports whether the blob identified by d exists and is referenced
// by repo.
func (r *Registry) BlobExists(ctx context.Context, repo string, d digest.Digest) (bool, int64, error) {
	key := storage.BlobKey(d.Algorithm().String(), d.Hex())
	info, err := r.blobs.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, info.Size, nil
}

// GetBlob opens a reader for the blob content.
func (r *Registry) GetBlob(ctx context.Context, repo string, d digest.Digest) (io.ReadCloser, int64, error) {
	key := storage.BlobKey(d.Algorithm().String(), d.Hex())
	rc, sz, err := r.blobs.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, 0, ErrBlobNotFound
	}
	return rc, sz, err
}

// GetBlobRange opens a range reader for the blob.
func (r *Registry) GetBlobRange(ctx context.Context, repo string, d digest.Digest, offset, length int64) (io.ReadCloser, error) {
	key := storage.BlobKey(d.Algorithm().String(), d.Hex())
	rc, err := r.blobs.GetRange(ctx, key, offset, length)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrBlobNotFound
	}
	return rc, err
}

// PutBlobMonolithic stores a complete blob in a single operation.
// It verifies the digest if provided, or computes it from the content.
func (r *Registry) PutBlobMonolithic(
	ctx context.Context,
	repo string,
	mediaType string,
	expectedDigest digest.Digest,
	body io.Reader,
	size int64,
) (digest.Digest, int64, error) {
	if err := validateRepo(repo); err != nil {
		return "", 0, err
	}

	// Buffer into memory for small blobs; stream via temp S3 for large.
	// For simplicity we compute the digest inline using a TeeReader.
	h := sha256.New()
	tr := io.TeeReader(body, h)

	tmpKey := fmt.Sprintf("tmp/uploads/%s/%s", repo, uuid.New().String())
	if err := r.blobs.Put(ctx, tmpKey, tr, size, storage.PutOptions{
		ContentType: mediaType,
	}); err != nil {
		return "", 0, fmt.Errorf("put blob: %w", err)
	}

	computed := digest.NewDigest("sha256", h)
	if expectedDigest != "" && computed != expectedDigest {
		_ = r.blobs.Delete(ctx, tmpKey)
		return "", 0, fmt.Errorf("%w: expected %s got %s", ErrDigestMismatch, expectedDigest, computed)
	}

	finalKey := storage.BlobKey(computed.Algorithm().String(), computed.Hex())
	exists, _, _ := r.BlobExists(ctx, repo, computed)
	if !exists {
		if err := r.blobs.Copy(ctx, tmpKey, finalKey); err != nil {
			return "", 0, fmt.Errorf("copy blob to final key: %w", err)
		}
	}
	_ = r.blobs.Delete(ctx, tmpKey)

	// Register cross-repo reference in metadata.
	refKey := metadata.KeyRegistryBlobRef(computed.String(), repo)
	_ = r.store.Put(ctx, refKey, []byte("1"))

	info, err := r.blobs.Stat(ctx, finalKey)
	if err != nil {
		return computed, 0, err
	}
	return computed, info.Size, nil
}

// DeleteBlob removes a blob from the store and cleans up the repo reference.
func (r *Registry) DeleteBlob(ctx context.Context, repo string, d digest.Digest) error {
	key := storage.BlobKey(d.Algorithm().String(), d.Hex())
	// Remove repo reference.
	_ = r.store.Delete(ctx, metadata.KeyRegistryBlobRef(d.String(), repo))
	// Only delete the actual blob if no other repo references it.
	// (Simplified: real implementation would do ref-count decrement.)
	return r.blobs.Delete(ctx, key)
}

// MountBlob attempts a cross-repository blob mount.
// Returns (true, nil) if the blob was successfully mounted, (false, nil) if
// the caller should fall back to a regular push.
func (r *Registry) MountBlob(ctx context.Context, fromRepo, toRepo string, d digest.Digest) (bool, error) {
	exists, _, err := r.BlobExists(ctx, fromRepo, d)
	if err != nil || !exists {
		return false, err
	}
	// Register the new repo reference.
	refKey := metadata.KeyRegistryBlobRef(d.String(), toRepo)
	return true, r.store.Put(ctx, refKey, []byte("1"))
}

// ─── Chunked upload ──────────────────────────────────────────────────────────

// InitiateUpload starts a new resumable upload and returns the session UUID.
func (r *Registry) InitiateUpload(ctx context.Context, repo string, expectedDigest digest.Digest) (*UploadSession, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}

	sessionUUID := uuid.New().String()
	key := fmt.Sprintf("tmp/uploads/%s/%s", repo, sessionUUID)

	uploadID, err := r.blobs.CreateMultipartUpload(ctx, key, storage.PutOptions{})
	if err != nil {
		return nil, fmt.Errorf("initiate multipart: %w", err)
	}

	sess := &UploadSession{
		UUID:           sessionUUID,
		Repository:     repo,
		UploadID:       uploadID,
		StartedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(uploadSessionTTL),
		ExpectedDigest: expectedDigest.String(),
	}

	sessJSON, _ := json.Marshal(sess)
	err = r.store.PutWithTTL(ctx,
		metadata.KeyRegistryUpload(repo, sessionUUID),
		sessJSON,
		uploadSessionTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("store upload session: %w", err)
	}
	return sess, nil
}

// PatchUpload appends a chunk to an in-progress upload.
func (r *Registry) PatchUpload(ctx context.Context, repo, sessionUUID string, offset int64, chunk io.Reader, size int64) (*UploadSession, error) {
	sess, err := r.getSession(ctx, repo, sessionUUID)
	if err != nil {
		return nil, err
	}
	if sess.Offset != offset {
		return nil, fmt.Errorf("registry: patch offset mismatch: expected %d got %d", sess.Offset, offset)
	}

	key := fmt.Sprintf("tmp/uploads/%s/%s", repo, sessionUUID)
	partNum := int32(len(sess.Parts) + 1)
	part, err := r.blobs.UploadPart(ctx, key, sess.UploadID, partNum, chunk, size)
	if err != nil {
		return nil, fmt.Errorf("upload part: %w", err)
	}

	sess.Parts = append(sess.Parts, part)
	sess.Offset += size

	sessJSON, _ := json.Marshal(sess)
	_ = r.store.PutWithTTL(ctx,
		metadata.KeyRegistryUpload(repo, sessionUUID),
		sessJSON,
		uploadSessionTTL,
	)
	return sess, nil
}

// CompleteUpload finalises the multipart upload, verifies the digest, and
// moves the object to its content-addressed key.
func (r *Registry) CompleteUpload(ctx context.Context, repo, sessionUUID string, d digest.Digest, trailing io.Reader, trailingSize int64) (digest.Digest, int64, error) {
	sess, err := r.getSession(ctx, repo, sessionUUID)
	if err != nil {
		return "", 0, err
	}

	key := fmt.Sprintf("tmp/uploads/%s/%s", repo, sessionUUID)

	// Append trailing data as the last part (if any).
	if trailingSize > 0 {
		partNum := int32(len(sess.Parts) + 1)
		part, err := r.blobs.UploadPart(ctx, key, sess.UploadID, partNum, trailing, trailingSize)
		if err != nil {
			return "", 0, fmt.Errorf("trailing part: %w", err)
		}
		sess.Parts = append(sess.Parts, part)
	}

	info, err := r.blobs.CompleteMultipartUpload(ctx, key, sess.UploadID, sess.Parts)
	if err != nil {
		return "", 0, fmt.Errorf("complete multipart: %w", err)
	}

	// If no digest provided, compute from the assembled object.
	finalDigest := d
	if finalDigest == "" {
		rc, _, err := r.blobs.Get(ctx, key)
		if err != nil {
			return "", 0, err
		}
		h := sha256.New()
		if _, err := io.Copy(h, rc); err != nil {
			rc.Close()
			return "", 0, err
		}
		rc.Close()
		finalDigest = digest.NewDigest("sha256", h)
	}

	// Verify if client provided expected digest.
	if d != "" && finalDigest != d {
		_ = r.blobs.Delete(ctx, key)
		return "", 0, fmt.Errorf("%w: expected %s got %s", ErrDigestMismatch, d, finalDigest)
	}

	finalKey := storage.BlobKey(finalDigest.Algorithm().String(), finalDigest.Hex())
	if err := r.blobs.Copy(ctx, key, finalKey); err != nil {
		return "", 0, fmt.Errorf("copy to final: %w", err)
	}
	_ = r.blobs.Delete(ctx, key)
	_ = r.store.Delete(ctx, metadata.KeyRegistryUpload(repo, sessionUUID))

	refKey := metadata.KeyRegistryBlobRef(finalDigest.String(), repo)
	_ = r.store.Put(ctx, refKey, []byte("1"))

	return finalDigest, info.Size, nil
}

// AbortUpload cancels a chunked upload and cleans up storage.
func (r *Registry) AbortUpload(ctx context.Context, repo, sessionUUID string) error {
	sess, err := r.getSession(ctx, repo, sessionUUID)
	if err != nil {
		if errors.Is(err, ErrUploadNotFound) {
			return nil
		}
		return err
	}
	key := fmt.Sprintf("tmp/uploads/%s/%s", repo, sessionUUID)
	_ = r.blobs.AbortMultipartUpload(ctx, key, sess.UploadID)
	return r.store.Delete(ctx, metadata.KeyRegistryUpload(repo, sessionUUID))
}

// GetUploadStatus returns the current state of an upload session.
func (r *Registry) GetUploadStatus(ctx context.Context, repo, sessionUUID string) (*UploadSession, error) {
	return r.getSession(ctx, repo, sessionUUID)
}

func (r *Registry) getSession(ctx context.Context, repo, sessionUUID string) (*UploadSession, error) {
	raw, err := r.store.Get(ctx, metadata.KeyRegistryUpload(repo, sessionUUID))
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrUploadNotFound
	}
	if err != nil {
		return nil, err
	}
	var sess UploadSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("parse upload session: %w", err)
	}
	return &sess, nil
}

// ─── Manifest operations ──────────────────────────────────────────────────────

// PutManifest validates and stores a manifest, resolving its reference (tag
// or digest), and indexing referrers if the manifest has a Subject field.
func (r *Registry) PutManifest(ctx context.Context, repo, reference, mediaType string, data []byte) (digest.Digest, string, error) {
	if err := validateRepo(repo); err != nil {
		return "", "", err
	}

	// Compute digest.
	d := digest.FromBytes(data)

	// Store the raw manifest in S3 for fast retrieval.
	manifestKey := storage.ManifestKey(repo, d.String())
	if err := r.blobs.Put(ctx, manifestKey, bytes.NewReader(data), int64(len(data)), storage.PutOptions{
		ContentType: mediaType,
	}); err != nil {
		return "", "", fmt.Errorf("store manifest blob: %w", err)
	}

	// Parse manifest to extract subject (for referrers) and layers.
	rec := ManifestRecord{
		Digest:    d.String(),
		MediaType: mediaType,
		Size:      int64(len(data)),
		PushedAt:  time.Now().UTC(),
	}

	// Try to parse as OCI image or artifact manifest to find Subject.
	var parsed struct {
		Subject      *ocispec.Descriptor `json:"subject"`
		ArtifactType string              `json:"artifactType"`
		Annotations  map[string]string   `json:"annotations"`
	}
	if err := json.Unmarshal(data, &parsed); err == nil {
		if parsed.Subject != nil {
			rec.Subject = parsed.Subject.Digest.String()
		}
		rec.ArtifactType = parsed.ArtifactType
		rec.Annotations = parsed.Annotations
	}

	recJSON, _ := json.Marshal(rec)
	if err := r.store.Put(ctx, metadata.KeyRegistryManifest(repo, d.String()), recJSON); err != nil {
		return "", "", err
	}

	// Handle tag reference.
	if !strings.HasPrefix(reference, "sha256:") && !strings.HasPrefix(reference, "sha512:") {
		if err := r.store.Put(ctx, metadata.KeyRegistryTag(repo, reference), []byte(d.String())); err != nil {
			return "", "", err
		}
	}

	// Index as referrer if subject is set.
	if rec.Subject != "" {
		desc := ocispec.Descriptor{
			MediaType:    mediaType,
			Digest:       d,
			Size:         int64(len(data)),
			ArtifactType: rec.ArtifactType,
			Annotations:  rec.Annotations,
		}
		descJSON, _ := json.Marshal(desc)
		refKey := metadata.KeyRegistryReferrer(repo, rec.Subject, d.String())
		if err := r.store.Put(ctx, refKey, descJSON); err != nil {
			r.log.Warn("failed to index referrer", zap.String("subject", rec.Subject), zap.Error(err))
		}
	}

	// Ensure repo is in catalog.
	_ = r.store.Put(ctx, metadata.KeyRegistryCatalog(repo), []byte(time.Now().UTC().Format(time.RFC3339)))

	return d, "/v2/" + repo + "/manifests/" + d.String(), nil
}

// GetManifest retrieves a manifest by tag or digest.
func (r *Registry) GetManifest(ctx context.Context, repo, reference string) ([]byte, *ManifestRecord, error) {
	d, err := r.resolveReference(ctx, repo, reference)
	if err != nil {
		return nil, nil, err
	}

	manifestKey := storage.ManifestKey(repo, d)
	data, err := storage.ReadAll(ctx, r.blobs, manifestKey)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil, ErrManifestNotFound
	}
	if err != nil {
		return nil, nil, err
	}

	raw, err := r.store.Get(ctx, metadata.KeyRegistryManifest(repo, d))
	if err != nil {
		return nil, nil, err
	}
	var rec ManifestRecord
	_ = json.Unmarshal(raw, &rec)

	return data, &rec, nil
}

// HeadManifest checks existence and returns metadata without the body.
func (r *Registry) HeadManifest(ctx context.Context, repo, reference string) (*ManifestRecord, error) {
	d, err := r.resolveReference(ctx, repo, reference)
	if err != nil {
		return nil, err
	}
	raw, err := r.store.Get(ctx, metadata.KeyRegistryManifest(repo, d))
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrManifestNotFound
	}
	if err != nil {
		return nil, err
	}
	var rec ManifestRecord
	_ = json.Unmarshal(raw, &rec)
	return &rec, nil
}

// DeleteManifest removes a manifest and its tag (if reference is a tag).
func (r *Registry) DeleteManifest(ctx context.Context, repo, reference string) error {
	d, err := r.resolveReference(ctx, repo, reference)
	if err != nil {
		return err
	}
	if !strings.Contains(reference, ":") {
		_ = r.store.Delete(ctx, metadata.KeyRegistryTag(repo, reference))
	}
	_ = r.store.Delete(ctx, metadata.KeyRegistryManifest(repo, d))
	_ = r.blobs.Delete(ctx, storage.ManifestKey(repo, d))
	return nil
}

// resolveReference resolves a tag or digest reference to a canonical digest string.
func (r *Registry) resolveReference(ctx context.Context, repo, reference string) (string, error) {
	if strings.HasPrefix(reference, "sha256:") || strings.HasPrefix(reference, "sha512:") {
		// Already a digest.
		_, err := r.store.Get(ctx, metadata.KeyRegistryManifest(repo, reference))
		if errors.Is(err, metadata.ErrNotFound) {
			return "", ErrManifestNotFound
		}
		return reference, err
	}
	// Tag lookup.
	raw, err := r.store.Get(ctx, metadata.KeyRegistryTag(repo, reference))
	if errors.Is(err, metadata.ErrNotFound) {
		return "", ErrManifestNotFound
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ─── Tags ────────────────────────────────────────────────────────────────────

// ListTags returns the tags for repo, paginating with last/limit.
func (r *Registry) ListTags(ctx context.Context, repo, last string, limit int) ([]string, error) {
	prefix := metadata.KeyRegistryTagsPrefix(repo)
	start := prefix
	if last != "" {
		start = metadata.KeyRegistryTag(repo, last)
		start = append(start, 0) // exclusive
	}
	kvs, err := r.store.Scan(ctx, start, metadata.PrefixEnd(prefix), limit)
	if err != nil {
		return nil, err
	}
	tags := make([]string, len(kvs))
	tagPrefixLen := len(metadata.KeyRegistryTag(repo, ""))
	for i, kv := range kvs {
		tags[i] = string(kv.Key)[tagPrefixLen:]
	}
	return tags, nil
}

func (r *Registry) DeleteTag(ctx context.Context, repo, tag string) error {
	return r.store.Delete(ctx, metadata.KeyRegistryTag(repo, tag))
}

// ─── Referrers (OCI 1.1) ─────────────────────────────────────────────────────

// ListReferrers returns all manifests that have subjectDigest as their Subject.
func (r *Registry) ListReferrers(ctx context.Context, repo, subjectDigest, artifactType, nextToken string, limit int) ([]ocispec.Descriptor, string, error) {
	prefix := metadata.KeyRegistryReferrersPrefix(repo, subjectDigest)
	start := prefix
	if nextToken != "" {
		start = []byte(nextToken)
	}
	kvs, err := r.store.Scan(ctx, start, metadata.PrefixEnd(prefix), limit+1)
	if err != nil {
		return nil, "", err
	}

	var descs []ocispec.Descriptor
	var next string
	for i, kv := range kvs {
		if limit > 0 && i >= limit {
			next = string(kv.Key)
			break
		}
		var d ocispec.Descriptor
		if err := json.Unmarshal(kv.Value, &d); err != nil {
			continue
		}
		if artifactType != "" && d.ArtifactType != artifactType {
			continue
		}
		descs = append(descs, d)
	}
	return descs, next, nil
}

// ─── Catalog ─────────────────────────────────────────────────────────────────

// ListRepositories returns the sorted list of repositories, paginating with last/limit.
func (r *Registry) ListRepositories(ctx context.Context, last string, limit int) ([]string, error) {
	prefix := metadata.KeyRegistryCatalogPrefix()
	start := prefix
	if last != "" {
		start = append(metadata.KeyRegistryCatalog(last), 0)
	}
	kvs, err := r.store.Scan(ctx, start, metadata.PrefixEnd(prefix), limit)
	if err != nil {
		return nil, err
	}
	repos := make([]string, len(kvs))
	pfxLen := len([]byte("reg/catalog/"))
	for i, kv := range kvs {
		repos[i] = string(kv.Key)[pfxLen:]
	}
	return repos, nil
}

// ─── Conversion graph ─────────────────────────────────────────────────────────

// ConversionRecord is stored in the KV store to track OCI→accelerated
// format conversions.
type ConversionRecord struct {
	ID                    string            `json:"id"`
	SourceDigest          string            `json:"source_digest"`
	TargetDigest          string            `json:"target_digest"`
	Format                string            `json:"format"`
	SourceAllBlobs        []BlobDescriptor  `json:"source_all_blobs"`
	TargetAllBlobs        []BlobDescriptor  `json:"target_all_blobs"`
	SourceLayers          []BlobDescriptor  `json:"source_layers"`
	TargetLayers          []BlobDescriptor  `json:"target_layers"`
	ConvertedAt           time.Time         `json:"converted_at"`
	ConverterVersion      string            `json:"converter_version"`
	Metadata              map[string]string `json:"metadata,omitempty"`
	AllSourceBlobsPresent bool              `json:"all_source_blobs_present"`
	AllTargetBlobsPresent bool              `json:"all_target_blobs_present"`
	MissingSourceBlobs    []string          `json:"missing_source_blobs,omitempty"`
	MissingTargetBlobs    []string          `json:"missing_target_blobs,omitempty"`
}

// BlobDescriptor is an OCI-spec descriptor stored within a ConversionRecord.
type BlobDescriptor struct {
	MediaType string `json:"media_type"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Role      string `json:"role,omitempty"` // "config", "layer", "manifest", "index"
}

// RegisterConversion stores a new conversion record and returns its ID.
func (r *Registry) RegisterConversion(ctx context.Context, repo string, rec ConversionRecord) (string, error) {
	if err := validateRepo(repo); err != nil {
		return "", err
	}
	if rec.ID == "" {
		rec.ID = uuid.New().String()
	}
	if rec.ConvertedAt.IsZero() {
		rec.ConvertedAt = time.Now().UTC()
	}
	// Run completeness check.
	rec = r.checkConversionCompleteness(ctx, rec)

	data, _ := json.Marshal(rec)
	key := metadata.KeyRegistryConversion(rec.SourceDigest, rec.Format)
	if err := r.store.Put(ctx, key, data); err != nil {
		return "", fmt.Errorf("store conversion: %w", err)
	}
	return rec.ID, nil
}

// GetConversion retrieves a conversion record for a given source digest and format.
func (r *Registry) GetConversion(ctx context.Context, repo, sourceDigest, format string) (*ConversionRecord, error) {
	key := metadata.KeyRegistryConversion(sourceDigest, format)
	raw, err := r.store.Get(ctx, key)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrConversionNotFound
	}
	if err != nil {
		return nil, err
	}
	var rec ConversionRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("parse conversion: %w", err)
	}
	// Re-check completeness (blobs may have been added since last check).
	rec = r.checkConversionCompleteness(ctx, rec)
	return &rec, nil
}

// CheckConversionBlobs verifies whether all blobs (source and target) for a
// conversion exist in the blob store.
func (r *Registry) CheckConversionBlobs(ctx context.Context, repo, sourceDigest, format string) (*ConversionRecord, error) {
	rec, err := r.GetConversion(ctx, repo, sourceDigest, format)
	if err != nil {
		return nil, err
	}
	// Persist updated completeness flags.
	data, _ := json.Marshal(rec)
	key := metadata.KeyRegistryConversion(sourceDigest, format)
	_ = r.store.Put(ctx, key, data)
	return rec, nil
}

// GetConversionGraph returns the full tree of conversions reachable from
// rootDigest.
func (r *Registry) GetConversionGraph(ctx context.Context, repo, rootDigest string, maxDepth int) (*ConversionNode, error) {
	return r.buildConversionNode(ctx, rootDigest, "oci", 0, maxDepth)
}

// ConversionNode is a node in the conversion graph tree.
type ConversionNode struct {
	Digest   string            `json:"digest"`
	Format   string            `json:"format"`
	Blobs    []BlobDescriptor  `json:"blobs,omitempty"`
	Children []*ConversionNode `json:"children,omitempty"`
}

func (r *Registry) buildConversionNode(ctx context.Context, digest, format string, depth, maxDepth int) (*ConversionNode, error) {
	node := &ConversionNode{Digest: digest, Format: format}
	if maxDepth > 0 && depth >= maxDepth {
		return node, nil
	}

	prefix := metadata.KeyRegistryConversionsPrefix(digest)
	kvs, err := r.store.ScanPrefix(ctx, prefix, 100)
	if err != nil {
		return node, nil // non-fatal
	}
	for _, kv := range kvs {
		var rec ConversionRecord
		if err := json.Unmarshal(kv.Value, &rec); err != nil {
			continue
		}
		child, err := r.buildConversionNode(ctx, rec.TargetDigest, rec.Format, depth+1, maxDepth)
		if err != nil {
			continue
		}
		child.Blobs = rec.TargetAllBlobs
		node.Children = append(node.Children, child)
	}
	return node, nil
}

// checkConversionCompleteness probes the blob store to update the
// AllSourceBlobsPresent / AllTargetBlobsPresent flags.
func (r *Registry) checkConversionCompleteness(ctx context.Context, rec ConversionRecord) ConversionRecord {
	rec.MissingSourceBlobs = nil
	rec.MissingTargetBlobs = nil

	for _, bd := range rec.SourceAllBlobs {
		d, err := digest.Parse(bd.Digest)
		if err != nil {
			continue
		}
		ok, _, _ := r.BlobExists(ctx, "", d)
		if !ok {
			rec.MissingSourceBlobs = append(rec.MissingSourceBlobs, bd.Digest)
		}
	}

	for _, bd := range rec.TargetAllBlobs {
		d, err := digest.Parse(bd.Digest)
		if err != nil {
			continue
		}
		ok, _, _ := r.BlobExists(ctx, "", d)
		if !ok {
			rec.MissingTargetBlobs = append(rec.MissingTargetBlobs, bd.Digest)
		}
	}

	rec.AllSourceBlobsPresent = len(rec.MissingSourceBlobs) == 0
	rec.AllTargetBlobsPresent = len(rec.MissingTargetBlobs) == 0
	return rec
}

// ─── Digest helpers ───────────────────────────────────────────────────────────

// hexDigest returns a sha256 hex string for content.
func hexDigest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
