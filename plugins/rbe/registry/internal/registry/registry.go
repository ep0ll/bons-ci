// Package registry implements the AccelRegistry: an OCI Distribution Spec
// compliant registry that additionally maintains an accel index, referrers
// index, and metadata store.
//
// Manifest ingest pipeline:
//
//  1. Validate well-formedness (JSON parse, required fields).
//  2. Verify all referenced blobs are present in the content store.
//  3. Run the accel detector to classify the manifest.
//  4. If accel: extract SourceRefs and index the AccelVariant.
//  5. If manifest has subject: record in the referrers index.
//  6. Store manifest blob and update the manifest index.
//  7. Upsert image metadata.
//
// All steps are idempotent — re-pushing the same manifest is safe.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	accelreg "github.com/bons/bons-ci/plugins/rbe/registry/internal/accel"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/dag"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/index"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/logger"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/metadata"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/referral"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/storage/memory"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Registry
// ────────────────────────────────────────────────────────────────────────────

// Registry is the central AccelRegistry implementation.
// It wires together the content store, accel index, metadata store,
// referrers store, and DAG traverser into a single cohesive registry.
type Registry struct {
	store     *memory.Store
	uploads   *memory.UploadStore
	manifests *memory.ManifestIndex
	accelIdx  *index.ShardedIndex
	meta      *metadata.Store
	referrers *referral.Store
	dag       *dag.Traverser
	accelReg  *accelreg.Registry
	log       *logger.Logger
	mu        sync.RWMutex // protects repo existence checks
	repos     map[string]struct{}
}

// Config configures the Registry.
type Config struct {
	// ExpectedSources is the approximate number of unique non-accelerated
	// images expected. Used to tune the bloom filter.
	ExpectedSources uint64

	// Log is the structured logger. Defaults to logger.NewNop() if nil.
	Log *logger.Logger
}

// New constructs a Registry from Config.
func New(cfg Config) (*Registry, error) {
	log := cfg.Log
	if log == nil {
		log = logger.NewNop()
	}
	if cfg.ExpectedSources == 0 {
		cfg.ExpectedSources = 100_000
	}

	// Build and register all accel handlers.
	ar := accelreg.NewRegistry(log)
	registerBuiltinHandlers(ar)

	return &Registry{
		store:     memory.New(),
		uploads:   memory.NewUploadStore(),
		manifests: memory.NewManifestIndex(),
		accelIdx:  index.NewShardedIndex(cfg.ExpectedSources),
		meta:      metadata.New(),
		referrers: referral.New(),
		dag:       dag.New(),
		accelReg:  ar,
		log:       log,
		repos:     make(map[string]struct{}),
	}, nil
}

// ── OCI Distribution Spec methods ─────────────────────────────────────────

// GetManifest returns the manifest descriptor and raw bytes for the given
// repository and reference (tag or digest string).
func (r *Registry) GetManifest(ctx context.Context, repo, ref string) (ocispec.Descriptor, []byte, error) {
	entry, ok := r.manifests.Get(repo, ref)
	if !ok {
		return ocispec.Descriptor{}, nil, errors.ManifestUnknown(ref)
	}
	rc, err := r.store.Get(ctx, entry.Digest())
	if err != nil {
		return ocispec.Descriptor{}, nil, errors.Wrap(errors.CodeManifestUnknown, 404, "manifest blob missing", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	desc := ocispec.Descriptor{
		MediaType: entry.MediaType(),
		Digest:    entry.Digest(),
		Size:      entry.Size(),
	}
	return desc, data, nil
}

// PutManifest stores a manifest and runs the full ingest pipeline.
func (r *Registry) PutManifest(ctx context.Context, repo, ref, mediaType string, rawManifest []byte) (digest.Digest, error) {
	if repo == "" {
		return "", errors.New(errors.CodeNameInvalid, 400, "empty repository name")
	}

	dgst := digest.Canonical.FromBytes(rawManifest)

	// 1. Parse the manifest.
	var manifest ocispec.Manifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return "", errors.Wrap(errors.CodeManifestInvalid, 400, "parsing manifest", err)
	}

	// 2. Verify all referenced blobs exist.
	if err := r.verifyManifestBlobs(ctx, manifest); err != nil {
		return "", err
	}

	// 3. Store the manifest blob.
	if err := r.store.Put(ctx, dgst, bytes.NewReader(rawManifest), int64(len(rawManifest))); err != nil {
		return "", fmt.Errorf("storing manifest blob: %w", err)
	}
	r.manifests.Put(repo, ref, dgst, mediaType, int64(len(rawManifest)))

	// 4. Ensure repo is registered.
	r.mu.Lock()
	r.repos[repo] = struct{}{}
	r.mu.Unlock()

	// 5. Fetch config blob for accel detection (ignore error — may be absent).
	var configBlob []byte
	if manifest.Config.Digest != "" {
		if rc, err := r.store.Get(ctx, manifest.Config.Digest); err == nil {
			configBlob, _ = io.ReadAll(rc)
			rc.Close()
		}
	}

	// 6. Detect acceleration type.
	accelType, err := r.accelReg.Detect(ctx, manifest, configBlob)
	if err != nil {
		r.log.Warn("accel detection failed", logger.Error(err), logger.String("repo", repo))
	}

	// 7. If accelerated, extract source refs and index.
	var sourceRefs []types.SourceRef
	if accelType != types.AccelUnknown {
		sourceRefs, err = r.accelReg.ExtractSourceRefs(ctx, accelType, manifest, configBlob)
		if err != nil {
			r.log.Warn("source ref extraction failed", logger.Error(err))
		}
		if len(sourceRefs) > 0 {
			totalSize := r.computeTotalSize(manifest)
			variant := types.AccelVariant{
				AccelType:      accelType,
				ManifestDigest: dgst,
				Repository:     repo,
				Tag:            tagFromRef(ref),
				Annotations:    manifest.Annotations,
				Size:           totalSize,
				CreatedAt:      time.Now(),
				Visibility:     visibilityFromAnnotations(manifest.Annotations),
				SourceRefs:     sourceRefs,
			}
			if manifest.Subject != nil {
				variant.IndexDigest = manifest.Subject.Digest
			}
			if err := r.accelIdx.Index(ctx, variant); err != nil {
				r.log.Error("failed to index accel variant", logger.Error(err))
			}
		}
	}

	// 8. OCI 1.1 referrers: if manifest has a subject, record it.
	if manifest.Subject != nil && manifest.Subject.Digest != "" {
		desc := ocispec.Descriptor{
			MediaType:    mediaType,
			ArtifactType: manifest.ArtifactType,
			Digest:       dgst,
			Size:         int64(len(rawManifest)),
			Annotations:  manifest.Annotations,
		}
		if err := r.referrers.AddReferrer(ctx, repo, manifest.Subject.Digest, desc); err != nil {
			r.log.Warn("referrers index update failed", logger.Error(err))
		}
	}

	// 9. Upsert metadata.
	sourceDgst := extractPrimarySourceDigest(sourceRefs)
	meta := types.ImageMetadata{
		Digest:       dgst,
		Repository:   repo,
		Tags:         tagsFromRef(ref),
		Visibility:   visibilityFromAnnotations(manifest.Annotations),
		IsAccel:      accelType != types.AccelUnknown,
		AccelType:    accelType,
		SourceDigest: sourceDgst,
		TotalSize:    r.computeTotalSize(manifest),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Annotations:  manifest.Annotations,
	}
	if err := r.meta.Put(ctx, meta); err != nil {
		r.log.Warn("metadata upsert failed", logger.Error(err))
	}

	r.log.Info("manifest stored",
		logger.String("repo", repo),
		logger.String("ref", ref),
		logger.String("digest", dgst.String()),
		logger.String("accelType", string(accelType)),
	)
	return dgst, nil
}

// DeleteManifest removes a manifest from the registry.
func (r *Registry) DeleteManifest(ctx context.Context, repo string, dgst digest.Digest) error {
	r.manifests.Delete(repo, dgst)
	if err := r.store.Delete(ctx, dgst); err != nil {
		return err
	}
	if err := r.meta.Delete(ctx, repo, dgst); err != nil {
		r.log.Warn("metadata delete failed", logger.Error(err))
	}
	if err := r.accelIdx.RemoveVariant(ctx, dgst, dgst); err != nil {
		r.log.Warn("accel index remove failed", logger.Error(err))
	}
	return nil
}

// GetBlob returns a read stream for a blob.
func (r *Registry) GetBlob(ctx context.Context, repo string, dgst digest.Digest) (io.ReadCloser, int64, error) {
	info, err := r.store.Info(ctx, dgst)
	if err != nil {
		return nil, 0, errors.BlobUnknown(dgst)
	}
	rc, err := r.store.Get(ctx, dgst)
	if err != nil {
		return nil, 0, errors.BlobUnknown(dgst)
	}
	return rc, info.Size, nil
}

// InitiateUpload starts a new blob upload session and returns a UUID.
func (r *Registry) InitiateUpload(_ context.Context, repo string) (string, error) {
	if repo == "" {
		return "", errors.New(errors.CodeNameInvalid, 400, "invalid name")
	}
	return r.uploads.Create(repo), nil
}

// ChunkUpload appends data to an in-progress upload.
func (r *Registry) ChunkUpload(_ context.Context, _, uuid string, data io.Reader, _, _ int64) error {
	b, err := io.ReadAll(data)
	if err != nil {
		return fmt.Errorf("reading chunk: %w", err)
	}
	return r.uploads.Append(uuid, b)
}

// FinalizeUpload completes a blob upload, verifies the digest, and stores it.
func (r *Registry) FinalizeUpload(ctx context.Context, repo, uuid string, dgst digest.Digest) error {
	data, err := r.uploads.Finalize(uuid, dgst)
	if err != nil {
		return errors.Wrap(errors.CodeDigestInvalid, 400, "finalize upload", err)
	}
	return r.store.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))
}

// GetTags returns a paginated list of tags for a repository.
func (r *Registry) GetTags(_ context.Context, repo, last string, n int) ([]string, error) {
	tags := r.manifests.Tags(repo)
	if len(tags) == 0 {
		return nil, nil
	}
	// Basic pagination
	start := 0
	if last != "" {
		for i, t := range tags {
			if t == last {
				start = i + 1
				break
			}
		}
	}
	if start >= len(tags) {
		return nil, nil
	}
	tags = tags[start:]
	if n > 0 && len(tags) > n {
		tags = tags[:n]
	}
	return tags, nil
}

// GetReferrers returns OCI 1.1 referrers for a digest.
func (r *Registry) GetReferrers(ctx context.Context, repo string, dgst digest.Digest, artifactType string) ([]ocispec.Descriptor, error) {
	return r.referrers.GetReferrers(ctx, repo, dgst, artifactType)
}

// ── Accel-specific methods ─────────────────────────────────────────────────

// QueryAccel returns all accel variants for a source (non-accel) digest.
func (r *Registry) QueryAccel(ctx context.Context, sourceDigest digest.Digest) (*types.AccelQueryResult, error) {
	result, err := r.accelIdx.Query(ctx, sourceDigest)
	if err != nil {
		return nil, fmt.Errorf("accel query: %w", err)
	}
	// Enrich with referrers (signatures, SBOMs attached to source)
	// Use "all repos" — search every repo by scanning the referrers store.
	// In production this would be scoped to a repo.
	return result, nil
}

// PullAccel resolves a PullRequest to a set of AccelVariants.
func (r *Registry) PullAccel(ctx context.Context, req types.PullRequest) (*types.PullResult, error) {
	result, err := r.QueryAccel(ctx, req.SourceDigest)
	if err != nil {
		return nil, err
	}
	if !result.Found {
		return nil, errors.AccelNotFound(req.SourceDigest)
	}

	var pulled []types.AccelVariant
	pullErrs := make(map[types.AccelType]string)

	typeFilter := make(map[types.AccelType]struct{}, len(req.AccelTypes))
	for _, t := range req.AccelTypes {
		typeFilter[t] = struct{}{}
	}

	for accelType, variants := range result.Variants {
		if len(typeFilter) > 0 {
			if _, ok := typeFilter[accelType]; !ok {
				continue
			}
		}
		for _, v := range variants {
			if req.Platform != nil && v.Platform != nil {
				if !platformMatches(req.Platform, v.Platform) {
					continue
				}
			}
			pulled = append(pulled, v)
		}
	}

	return &types.PullResult{
		SourceDigest: req.SourceDigest,
		Pulled:       pulled,
		Errors:       pullErrs,
	}, nil
}

// GetDAG returns the full content DAG for a digest.
func (r *Registry) GetDAG(ctx context.Context, repo string, dgst digest.Digest) (*types.DAGQueryResult, error) {
	// Determine media type
	entry, ok := r.manifests.Get(repo, dgst.String())
	mt := ""
	if ok {
		mt = entry.MediaType()
	}
	desc := ocispec.Descriptor{
		Digest:    dgst,
		MediaType: mt,
	}
	return r.dag.Traverse(ctx, repo, desc, r.store)
}

// GetImageMetadata returns rich metadata for a specific image.
func (r *Registry) GetImageMetadata(ctx context.Context, repo string, dgst digest.Digest) (*types.ImageMetadata, error) {
	return r.meta.Get(ctx, repo, dgst)
}

// IndexStats returns aggregate statistics about the accel index.
func (r *Registry) IndexStats() types.IndexStats {
	return r.accelIdx.Stats()
}

// ────────────────────────────────────────────────────────────────────────────
// Private helpers
// ────────────────────────────────────────────────────────────────────────────

func (r *Registry) verifyManifestBlobs(ctx context.Context, manifest ocispec.Manifest) error {
	blobs := make([]ocispec.Descriptor, 0, len(manifest.Layers)+1)
	if manifest.Config.Digest != "" {
		blobs = append(blobs, manifest.Config)
	}
	blobs = append(blobs, manifest.Layers...)

	for _, desc := range blobs {
		exists, err := r.store.Exists(ctx, desc.Digest)
		if err != nil {
			return fmt.Errorf("checking blob %s: %w", desc.Digest, err)
		}
		if !exists {
			return errors.New(errors.CodeManifestBlobUnknown, 400,
				fmt.Sprintf("blob %s not found in registry", desc.Digest))
		}
	}
	return nil
}

func (r *Registry) computeTotalSize(manifest ocispec.Manifest) int64 {
	var total int64
	total += manifest.Config.Size
	for _, l := range manifest.Layers {
		total += l.Size
	}
	return total
}

func extractPrimarySourceDigest(refs []types.SourceRef) digest.Digest {
	for _, ref := range refs {
		if ref.Kind == types.SourceRefManifest || ref.Kind == types.SourceRefIndex {
			return ref.Digest
		}
	}
	return ""
}

func visibilityFromAnnotations(ann map[string]string) types.Visibility {
	v, ok := ann[types.AnnotationVisibility]
	if !ok {
		return types.VisibilityPublic // default to public
	}
	switch types.Visibility(v) {
	case types.VisibilityPublic, types.VisibilityPrivate:
		return types.Visibility(v)
	}
	return types.VisibilityUnknown
}

func tagFromRef(ref string) string {
	// If ref starts with "sha256:", it's a digest reference, not a tag.
	if len(ref) > 7 && ref[:7] == "sha256:" {
		return ""
	}
	return ref
}

func tagsFromRef(ref string) []string {
	t := tagFromRef(ref)
	if t == "" {
		return nil
	}
	return []string{t}
}

func platformMatches(want, got *ocispec.Platform) bool {
	if want.OS != "" && want.OS != got.OS {
		return false
	}
	if want.Architecture != "" && want.Architecture != got.Architecture {
		return false
	}
	return true
}

// registerBuiltinHandlers adds all built-in accel handlers to the registry.
// Add new handlers here when new accel types are implemented.
func registerBuiltinHandlers(ar *accelreg.Registry) {
	// Import is done inline via anonymous registration to keep this file
	// self-contained. In a real project each handler is imported explicitly.
	// These are registered in detection-priority order.
	ar.Register(nydusHandler{})
	ar.Register(sociHandler{})
	ar.Register(estargzHandler{})
	ar.Register(overlayBDHandler{})
}

// ── Minimal inline handler adapters (wire-up layer) ───────────────────────
// These thin wrappers adapt the per-package handler types to the
// accelreg.Registry.Register() call without creating import cycles.

type nydusHandler struct{}
type sociHandler struct{}
type estargzHandler struct{}
type overlayBDHandler struct{}

func (nydusHandler) Name() types.AccelType { return types.AccelNydus }
func (nydusHandler) Detect(ctx context.Context, m ocispec.Manifest, c []byte) (types.AccelType, bool, error) {
	return detectNydus(ctx, m, c)
}
func (nydusHandler) ExtractSourceRefs(ctx context.Context, m ocispec.Manifest, c []byte) ([]types.SourceRef, error) {
	return extractNydusRefs(ctx, m, c)
}
func (nydusHandler) Validate(ctx context.Context, m ocispec.Manifest) error {
	return validateNydus(ctx, m)
}

func (sociHandler) Name() types.AccelType { return types.AccelSOCI }
func (sociHandler) Detect(ctx context.Context, m ocispec.Manifest, c []byte) (types.AccelType, bool, error) {
	return detectSOCI(ctx, m, c)
}
func (sociHandler) ExtractSourceRefs(ctx context.Context, m ocispec.Manifest, c []byte) ([]types.SourceRef, error) {
	return extractSOCIRefs(ctx, m, c)
}
func (sociHandler) Validate(ctx context.Context, m ocispec.Manifest) error {
	return validateSOCI(ctx, m)
}

func (estargzHandler) Name() types.AccelType { return types.AccelEstargz }
func (estargzHandler) Detect(ctx context.Context, m ocispec.Manifest, c []byte) (types.AccelType, bool, error) {
	return detectEstargz(ctx, m, c)
}
func (estargzHandler) ExtractSourceRefs(ctx context.Context, m ocispec.Manifest, c []byte) ([]types.SourceRef, error) {
	return extractEstargzRefs(ctx, m, c)
}
func (estargzHandler) Validate(ctx context.Context, m ocispec.Manifest) error {
	return validateEstargz(ctx, m)
}

func (overlayBDHandler) Name() types.AccelType { return types.AccelOverlayBD }
func (overlayBDHandler) Detect(ctx context.Context, m ocispec.Manifest, c []byte) (types.AccelType, bool, error) {
	return detectOverlayBD(ctx, m, c)
}
func (overlayBDHandler) ExtractSourceRefs(ctx context.Context, m ocispec.Manifest, c []byte) ([]types.SourceRef, error) {
	return extractOverlayBDRefs(ctx, m, c)
}
func (overlayBDHandler) Validate(ctx context.Context, m ocispec.Manifest) error {
	return validateOverlayBD(ctx, m)
}
