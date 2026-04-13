package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
	"github.com/google/uuid"
)

// ── Key scheme ────────────────────────────────────────────────────────────────
//
//   registries/<repo>/blobs/<digest>                → JSON BlobDescriptor
//   registries/<repo>/manifests/digest/<digest>     → JSON ManifestDescriptor
//   registries/<repo>/manifests/tag/<tag>           → digest string
//   registries/<repo>/uploads/<uuid>                → JSON UploadSession
//   conversions/<source_digest>/<format>/<id>       → JSON ConversionRecord
//   referrers/<repo>/<subject_digest>/<ref_digest>  → JSON ManifestDescriptor
//

const (
	keyBlob       = "registries/%s/blobs/%s"
	keyManifest   = "registries/%s/manifests/digest/%s"
	keyTag        = "registries/%s/manifests/tag/%s"
	keyUpload     = "registries/%s/uploads/%s"
	keyConversion = "conversions/%s/%s/%s"
	keyReferrer   = "referrers/%s/%s/%s"

	uploadTTL = 1 * time.Hour
)

// Registry is the central object managing all OCI registry operations.
type Registry struct {
	store storage.Store
	meta  metadata.Store
}

// New creates a new Registry.
func New(store storage.Store, meta metadata.Store) *Registry {
	return &Registry{store: store, meta: meta}
}

// ─────────────────────────────────────────────────────────────────────────────
// Blob operations
// ─────────────────────────────────────────────────────────────────────────────

// StatBlob returns metadata about a blob.
func (r *Registry) StatBlob(ctx context.Context, repo, digest string) (*models.BlobDescriptor, error) {
	key := []byte(fmt.Sprintf(keyBlob, repo, digest))
	data, err := r.meta.Get(ctx, key)
	if err != nil {
		if err == metadata.ErrKeyNotFound {
			ok, size, err2 := r.store.Exists(ctx, digest)
			if err2 != nil || !ok {
				return nil, errors.NewBlobUnknown(digest)
			}
			return &models.BlobDescriptor{Digest: digest, Size: size}, nil
		}
		return nil, err
	}
	var desc models.BlobDescriptor
	return &desc, json.Unmarshal(data, &desc)
}

// PutBlob stores a blob descriptor in the metadata index.
func (r *Registry) PutBlob(ctx context.Context, repo string, desc *models.BlobDescriptor) error {
	if desc.CreatedAt.IsZero() {
		desc.CreatedAt = time.Now()
	}
	desc.Repository = repo
	data, _ := json.Marshal(desc)
	return r.meta.Put(ctx, []byte(fmt.Sprintf(keyBlob, repo, desc.Digest)), data)
}

// DeleteBlob removes a blob from both metadata and blob store.
func (r *Registry) DeleteBlob(ctx context.Context, repo, digest string) error {
	_ = r.meta.Delete(ctx, []byte(fmt.Sprintf(keyBlob, repo, digest)))
	return r.store.Delete(ctx, digest)
}

// ListBlobs returns all blobs for a repository, optionally limited to one manifest.
func (r *Registry) ListBlobs(ctx context.Context, repo, manifestDigest string, limit int) ([]models.BlobDescriptor, error) {
	if manifestDigest != "" {
		m, err := r.GetManifest(ctx, repo, manifestDigest)
		if err != nil {
			return nil, err
		}
		var blobs []models.BlobDescriptor
		if m.Config != nil {
			blobs = append(blobs, *m.Config)
		}
		return append(blobs, m.Blobs...), nil
	}
	prefix := []byte(fmt.Sprintf("registries/%s/blobs/", repo))
	pairs, err := r.meta.ScanPrefix(ctx, prefix, limit)
	if err != nil {
		return nil, err
	}
	var blobs []models.BlobDescriptor
	for _, p := range pairs {
		var d models.BlobDescriptor
		if json.Unmarshal(p.Value, &d) == nil {
			blobs = append(blobs, d)
		}
	}
	return blobs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Chunked upload (OCI Distribution Spec §10)
// ─────────────────────────────────────────────────────────────────────────────

// InitiateUpload creates a new upload session and returns it.
func (r *Registry) InitiateUpload(ctx context.Context, repo string) (*models.UploadSession, error) {
	id := uuid.New().String()
	sess := &models.UploadSession{
		UUID:       id,
		Repository: repo,
		StorageKey: "upload:" + id,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(uploadTTL),
	}
	if err := r.store.InitiateUpload(ctx, id, map[string]string{"repo": repo}); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(sess)
	return sess, r.meta.Put(ctx, []byte(fmt.Sprintf(keyUpload, repo, id)), data,
		metadata.WithTTL(int64(uploadTTL.Seconds())))
}

// GetUploadSession retrieves an active upload session.
func (r *Registry) GetUploadSession(ctx context.Context, repo, uploadUUID string) (*models.UploadSession, error) {
	data, err := r.meta.Get(ctx, []byte(fmt.Sprintf(keyUpload, repo, uploadUUID)))
	if err != nil {
		return nil, errors.NewUploadUnknown(uploadUUID)
	}
	var sess models.UploadSession
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, errors.NewUploadUnknown(uploadUUID)
	}
	return &sess, nil
}

// UploadChunk writes one part of a chunked upload.
func (r *Registry) UploadChunk(ctx context.Context, repo, uploadUUID string, partNum int, data []byte, size int64) (string, error) {
	sess, err := r.GetUploadSession(ctx, repo, uploadUUID)
	if err != nil {
		return "", err
	}
	etag, err := r.store.UploadPart(ctx, uploadUUID, partNum, strings.NewReader(string(data)), size)
	if err != nil {
		return "", err
	}
	sess.Offset += size
	sess.UpdatedAt = time.Now()
	raw, _ := json.Marshal(sess)
	_ = r.meta.Put(ctx, []byte(fmt.Sprintf(keyUpload, repo, uploadUUID)), raw,
		metadata.WithTTL(int64(uploadTTL.Seconds())))
	return etag, nil
}

// CompleteUpload finalises a chunked upload.
func (r *Registry) CompleteUpload(ctx context.Context, repo, uploadUUID, digest string, parts []storage.Part) (*models.BlobDescriptor, error) {
	sess, err := r.GetUploadSession(ctx, repo, uploadUUID)
	if err != nil {
		return nil, err
	}
	if err := r.store.CompleteUpload(ctx, uploadUUID, digest, parts); err != nil {
		return nil, err
	}
	info, err := r.store.Stat(ctx, digest)
	if err != nil {
		return nil, err
	}
	desc := &models.BlobDescriptor{Digest: digest, Size: info.Size, Repository: repo, CreatedAt: time.Now()}
	if err := r.PutBlob(ctx, repo, desc); err != nil {
		return nil, err
	}
	_ = r.meta.Delete(ctx, []byte(fmt.Sprintf(keyUpload, sess.Repository, uploadUUID)))
	return desc, nil
}

// AbortUpload cancels an upload session.
func (r *Registry) AbortUpload(ctx context.Context, repo, uploadUUID string) error {
	_ = r.store.AbortUpload(ctx, uploadUUID)
	return r.meta.Delete(ctx, []byte(fmt.Sprintf(keyUpload, repo, uploadUUID)))
}

// ─────────────────────────────────────────────────────────────────────────────
// Manifest operations
// ─────────────────────────────────────────────────────────────────────────────

// PutManifest stores a manifest and updates the tag index.
func (r *Registry) PutManifest(ctx context.Context, repo, reference string, raw []byte, mediaType string) (*models.ManifestDescriptor, error) {
	desc, err := parseManifest(repo, reference, raw, mediaType)
	if err != nil {
		return nil, err
	}
	// Store raw manifest bytes.
	if err := r.store.Put(ctx, desc.Digest, strings.NewReader(string(raw)), int64(len(raw)), storage.PutOptions{}); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(desc)
	if err := r.meta.Put(ctx, []byte(fmt.Sprintf(keyManifest, repo, desc.Digest)), data); err != nil {
		return nil, err
	}
	// Tag mapping.
	if !strings.HasPrefix(reference, "sha256:") && !strings.HasPrefix(reference, "blake3:") {
		_ = r.meta.Put(ctx, []byte(fmt.Sprintf(keyTag, repo, reference)), []byte(desc.Digest))
		desc.Tag = reference
	}
	// Index layers and config.
	for i := range desc.Blobs {
		b := desc.Blobs[i]
		b.Repository = repo
		_ = r.PutBlob(ctx, repo, &b)
	}
	if desc.Config != nil {
		cfg := *desc.Config
		cfg.Repository = repo
		_ = r.PutBlob(ctx, repo, &cfg)
	}
	// OCI 1.1 referrers.
	if desc.Subject != nil {
		refKey := []byte(fmt.Sprintf(keyReferrer, repo, desc.Subject.Digest, desc.Digest))
		_ = r.meta.Put(ctx, refKey, data)
	}
	return desc, nil
}

// GetManifest retrieves a manifest by digest or tag.
func (r *Registry) GetManifest(ctx context.Context, repo, reference string) (*models.ManifestDescriptor, error) {
	digest := reference
	if !strings.Contains(reference, ":") {
		d, err := r.meta.Get(ctx, []byte(fmt.Sprintf(keyTag, repo, reference)))
		if err != nil {
			return nil, errors.NewManifestUnknown(reference)
		}
		digest = string(d)
	}
	data, err := r.meta.Get(ctx, []byte(fmt.Sprintf(keyManifest, repo, digest)))
	if err != nil {
		return nil, errors.NewManifestUnknown(reference)
	}
	var desc models.ManifestDescriptor
	return &desc, json.Unmarshal(data, &desc)
}

// DeleteManifest removes a manifest and its tag.
func (r *Registry) DeleteManifest(ctx context.Context, repo, reference string) error {
	m, err := r.GetManifest(ctx, repo, reference)
	if err != nil {
		return err
	}
	_ = r.meta.Delete(ctx, []byte(fmt.Sprintf(keyManifest, repo, m.Digest)))
	if m.Tag != "" {
		_ = r.meta.Delete(ctx, []byte(fmt.Sprintf(keyTag, repo, m.Tag)))
	}
	return r.store.Delete(ctx, m.Digest)
}

// ListManifests returns all manifests in a repository.
func (r *Registry) ListManifests(ctx context.Context, repo string, limit int) ([]models.ManifestDescriptor, error) {
	pairs, err := r.meta.ScanPrefix(ctx, []byte(fmt.Sprintf("registries/%s/manifests/digest/", repo)), limit)
	if err != nil {
		return nil, err
	}
	var manifests []models.ManifestDescriptor
	for _, p := range pairs {
		var m models.ManifestDescriptor
		if json.Unmarshal(p.Value, &m) == nil {
			manifests = append(manifests, m)
		}
	}
	return manifests, nil
}

// ListTags returns all tags for a repository.
func (r *Registry) ListTags(ctx context.Context, repo string) ([]string, error) {
	base := fmt.Sprintf("registries/%s/manifests/tag/", repo)
	pairs, err := r.meta.ScanPrefix(ctx, []byte(base), 0)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, p := range pairs {
		tags = append(tags, strings.TrimPrefix(string(p.Key), base))
	}
	return tags, nil
}

// GetRawManifest returns raw manifest bytes.
func (r *Registry) GetRawManifest(ctx context.Context, repo, reference string) ([]byte, error) {
	m, err := r.GetManifest(ctx, repo, reference)
	if err != nil {
		return nil, err
	}
	rc, _, err := r.store.Get(ctx, m.Digest, storage.GetOptions{})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf strings.Builder
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// GetReferrers returns OCI 1.1 referrers.
func (r *Registry) GetReferrers(ctx context.Context, repo, subjectDigest, artifactType string) ([]models.ManifestDescriptor, error) {
	prefix := []byte(fmt.Sprintf("referrers/%s/%s/", repo, subjectDigest))
	pairs, err := r.meta.ScanPrefix(ctx, prefix, 0)
	if err != nil {
		return nil, err
	}
	var refs []models.ManifestDescriptor
	for _, p := range pairs {
		var m models.ManifestDescriptor
		if json.Unmarshal(p.Value, &m) == nil {
			if artifactType == "" || m.ArtifactType == artifactType {
				refs = append(refs, m)
			}
		}
	}
	return refs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Conversion tracking
// ─────────────────────────────────────────────────────────────────────────────

// RecordConversion persists a format conversion record.
func (r *Registry) RecordConversion(ctx context.Context, rec *models.ConversionRecord) error {
	if rec.ID == "" {
		rec.ID = uuid.New().String()
	}
	if rec.ConvertedAt.IsZero() {
		rec.ConvertedAt = time.Now()
	}
	rec.SharedDigests, rec.SourceOnlyDigests, rec.TargetOnlyDigests = blobDiff(rec.SourceBlobs, rec.TargetBlobs)
	data, _ := json.Marshal(rec)
	key := []byte(fmt.Sprintf(keyConversion, rec.SourceDigest, string(rec.TargetFormat), rec.ID))
	return r.meta.Put(ctx, key, data)
}

// GetConversion retrieves a conversion record by ID.
func (r *Registry) GetConversion(ctx context.Context, id string) (*models.ConversionRecord, error) {
	pairs, err := r.meta.ScanPrefix(ctx, []byte("conversions/"), 0)
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		if strings.HasSuffix(string(p.Key), "/"+id) {
			var rec models.ConversionRecord
			if json.Unmarshal(p.Value, &rec) == nil {
				return &rec, nil
			}
		}
	}
	return nil, errors.ErrNotFound
}

// ListConversions returns conversions for a source digest and format.
func (r *Registry) ListConversions(ctx context.Context, sourceDigest string, targetFormat models.ImageFormat) ([]models.ConversionRecord, error) {
	prefix := "conversions/"
	if sourceDigest != "" {
		prefix = fmt.Sprintf("conversions/%s/%s/", sourceDigest, string(targetFormat))
	}
	pairs, err := r.meta.ScanPrefix(ctx, []byte(prefix), 0)
	if err != nil {
		return nil, err
	}
	var recs []models.ConversionRecord
	for _, p := range pairs {
		var rec models.ConversionRecord
		if json.Unmarshal(p.Value, &rec) == nil {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}

// CheckConversionExists checks whether a conversion exists and optionally verifies blob presence.
func (r *Registry) CheckConversionExists(ctx context.Context, sourceDigest string, targetFormat models.ImageFormat, verifyBlobs bool) (bool, *models.ConversionRecord, []string) {
	prefix := fmt.Sprintf("conversions/%s/%s/", sourceDigest, string(targetFormat))
	pairs, _ := r.meta.ScanPrefix(ctx, []byte(prefix), 1)
	if len(pairs) == 0 {
		return false, nil, nil
	}
	var rec models.ConversionRecord
	if err := json.Unmarshal(pairs[0].Value, &rec); err != nil {
		return false, nil, nil
	}
	if !verifyBlobs {
		return true, &rec, nil
	}
	var missing []string
	for _, b := range rec.TargetBlobs {
		if ok, _, _ := r.store.Exists(ctx, b.Digest); !ok {
			missing = append(missing, b.Digest)
		}
	}
	return true, &rec, missing
}

// ConversionBlobDiff returns the added/removed/shared blob sets between two manifests.
func (r *Registry) ConversionBlobDiff(ctx context.Context, srcDigest, dstDigest string) (added, removed, shared []models.BlobDescriptor, err error) {
	pairs, _ := r.meta.ScanPrefix(ctx, []byte("conversions/"+srcDigest+"/"), 0)
	for _, p := range pairs {
		var rec models.ConversionRecord
		if json.Unmarshal(p.Value, &rec) == nil && rec.TargetDigest == dstDigest {
			sh, src2, dst2 := blobDiffDescs(rec.SourceBlobs, rec.TargetBlobs)
			return dst2, src2, sh, nil
		}
	}
	return nil, nil, nil, errors.ErrNotFound
}

// ListRepositories returns all known repository names.
func (r *Registry) ListRepositories(ctx context.Context, prefix string, limit int) ([]string, error) {
	p := "registries/"
	if prefix != "" {
		p += prefix
	}
	pairs, err := r.meta.ScanPrefix(ctx, []byte(p), 0)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, kv := range pairs {
		parts := strings.SplitN(string(kv.Key), "/", 3)
		if len(parts) >= 2 {
			seen[parts[1]] = struct{}{}
		}
	}
	repos := make([]string, 0, len(seen))
	for repo := range seen {
		repos = append(repos, repo)
		if limit > 0 && len(repos) >= limit {
			break
		}
	}
	return repos, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Manifest parsing helpers
// ─────────────────────────────────────────────────────────────────────────────

func computeDigest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func parseManifest(repo, reference string, raw []byte, mediaType string) (*models.ManifestDescriptor, error) {
	digest := computeDigest(raw)

	var m struct {
		MediaType    string            `json:"mediaType"`
		Config       *ociDescriptor    `json:"config"`
		Layers       []ociDescriptor   `json:"layers"`
		Manifests    []ociDescriptor   `json:"manifests"`
		Subject      *ociDescriptor    `json:"subject"`
		ArtifactType string            `json:"artifactType"`
		Annotations  map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if mediaType == "" {
		mediaType = m.MediaType
	}
	desc := &models.ManifestDescriptor{
		Digest:       digest,
		MediaType:    mediaType,
		Size:         int64(len(raw)),
		Repository:   repo,
		Annotations:  m.Annotations,
		ArtifactType: m.ArtifactType,
		Format:       detectFormat(mediaType),
		CreatedAt:    time.Now(),
	}
	if m.Config != nil {
		desc.Config = &models.BlobDescriptor{
			Digest:    m.Config.Digest,
			MediaType: m.Config.MediaType,
			Size:      m.Config.Size,
			Role:      "config",
			Format:    detectLayerFormat(m.Config.MediaType, m.Config.Annotations),
		}
	}
	for _, l := range m.Layers {
		desc.Blobs = append(desc.Blobs, models.BlobDescriptor{
			Digest:      l.Digest,
			MediaType:   l.MediaType,
			Size:        l.Size,
			Annotations: l.Annotations,
			Role:        "layer",
			Format:      detectLayerFormat(l.MediaType, l.Annotations),
		})
	}
	for _, mf := range m.Manifests {
		b := models.BlobDescriptor{
			Digest:    mf.Digest,
			MediaType: mf.MediaType,
			Size:      mf.Size,
			Role:      "manifest",
		}
		if mf.Platform != nil {
			desc.Platform = &models.Platform{OS: mf.Platform.OS, Arch: mf.Platform.Architecture, Variant: mf.Platform.Variant}
		}
		desc.Blobs = append(desc.Blobs, b)
	}
	if m.Subject != nil {
		desc.Subject = &models.BlobDescriptor{Digest: m.Subject.Digest, MediaType: m.Subject.MediaType, Size: m.Subject.Size}
	}
	if !strings.HasPrefix(reference, "sha256:") {
		desc.Tag = reference
	}
	return desc, nil
}

type ociDescriptor struct {
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
	Platform    *ociPlatform      `json:"platform"`
}

type ociPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
}

func detectFormat(mediaType string) models.ImageFormat {
	switch {
	case strings.Contains(mediaType, "nydus"):
		return models.ImageFormatNydus
	case strings.Contains(mediaType, "estargz"):
		return models.ImageFormatEStargz
	case strings.Contains(mediaType, "zstd"):
		return models.ImageFormatZstd
	case strings.Contains(mediaType, "overlaybd"):
		return models.ImageFormatOverlayBD
	case strings.Contains(mediaType, "docker"):
		return models.ImageFormatDocker
	default:
		return models.ImageFormatOCI
	}
}

func detectLayerFormat(mediaType string, annotations map[string]string) models.ImageFormat {
	if annotations != nil {
		if _, ok := annotations["containerd.io/snapshot/nydus-bootstrap"]; ok {
			return models.ImageFormatNydus
		}
		if _, ok := annotations["containerd.io/snapshot/overlaybd"]; ok {
			return models.ImageFormatOverlayBD
		}
	}
	return detectFormat(mediaType)
}

func blobDiff(src, dst []models.BlobDescriptor) (shared, srcOnly, dstOnly []string) {
	srcMap := map[string]struct{}{}
	for _, b := range src {
		srcMap[b.Digest] = struct{}{}
	}
	dstMap := map[string]struct{}{}
	for _, b := range dst {
		dstMap[b.Digest] = struct{}{}
	}
	for d := range srcMap {
		if _, ok := dstMap[d]; ok {
			shared = append(shared, d)
		} else {
			srcOnly = append(srcOnly, d)
		}
	}
	for d := range dstMap {
		if _, ok := srcMap[d]; !ok {
			dstOnly = append(dstOnly, d)
		}
	}
	return
}

func blobDiffDescs(src, dst []models.BlobDescriptor) (shared, srcOnly, dstOnly []models.BlobDescriptor) {
	srcMap := map[string]models.BlobDescriptor{}
	for _, b := range src {
		srcMap[b.Digest] = b
	}
	dstMap := map[string]models.BlobDescriptor{}
	for _, b := range dst {
		dstMap[b.Digest] = b
	}
	for d, b := range srcMap {
		if _, ok := dstMap[d]; ok {
			shared = append(shared, b)
		} else {
			srcOnly = append(srcOnly, b)
		}
	}
	for d, b := range dstMap {
		if _, ok := srcMap[d]; !ok {
			dstOnly = append(dstOnly, b)
		}
	}
	return
}
