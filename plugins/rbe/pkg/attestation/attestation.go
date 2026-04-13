// Package attestation manages SBOM (SPDX/CycloneDX), SLSA provenance,
// cosign/sigstore signing, and OCI referrer-based attestation storage.
package attestation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
	"github.com/google/uuid"
)

// Key scheme:
//   attestation/<subject_digest>/<type>/<id> → JSON Attestation
//   slsa/<subject_digest>                    → JSON SLSAProvenance

const (
	keyAttestation = "attestation/%s/%s/%s"
	keySLSA        = "slsa/%s"
)

// Service manages attestations and provenance records.
type Service struct {
	meta  metadata.Store
	store storage.Store
}

// New creates an attestation Service.
func New(meta metadata.Store, store storage.Store) *Service {
	return &Service{meta: meta, store: store}
}

// Attach stores an attestation (DSSE envelope or raw payload).
func (s *Service) Attach(ctx context.Context, att *models.Attestation) error {
	if att.ID == "" {
		att.ID = uuid.New().String()
	}
	if att.SignedAt.IsZero() {
		att.SignedAt = time.Now()
	}

	// Store the raw payload in blob store for large SBOMs.
	if len(att.Payload) > 0 {
		blobKey := fmt.Sprintf("attestation-payload:%s", att.ID)
		if err := s.store.Put(ctx, blobKey, strings.NewReader(string(att.Payload)), int64(len(att.Payload)), storage.PutOptions{}); err != nil {
			return fmt.Errorf("attestation: store payload: %w", err)
		}
		// Keep only the blob key in metadata, not the full payload.
		att.Payload = []byte(blobKey)
	}

	data, err := json.Marshal(att)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf(keyAttestation, att.SubjectDigest, string(att.Type), att.ID))
	return s.meta.Put(ctx, key, data)
}

// GetAttestations retrieves all attestations for a subject digest.
func (s *Service) GetAttestations(ctx context.Context, subjectDigest string, attType models.AttestationType) ([]*models.Attestation, error) {
	var prefix string
	if attType != "" {
		prefix = fmt.Sprintf("attestation/%s/%s/", subjectDigest, string(attType))
	} else {
		prefix = fmt.Sprintf("attestation/%s/", subjectDigest)
	}
	pairs, err := s.meta.ScanPrefix(ctx, []byte(prefix), 0)
	if err != nil {
		return nil, err
	}
	var atts []*models.Attestation
	for _, p := range pairs {
		var att models.Attestation
		if err := json.Unmarshal(p.Value, &att); err != nil {
			continue
		}
		// Hydrate payload from blob store.
		if blobKey := string(att.Payload); strings.HasPrefix(blobKey, "attestation-payload:") {
			rc, _, err := s.store.Get(ctx, blobKey, storage.GetOptions{})
			if err == nil {
				var buf strings.Builder
				buf.ReadFrom(rc)
				rc.Close()
				att.Payload = []byte(buf.String())
			}
		}
		atts = append(atts, &att)
	}
	return atts, nil
}

// ListAttestations returns all attestations for a repository.
func (s *Service) ListAttestations(ctx context.Context, subjectRepo string, attType models.AttestationType, limit int) ([]*models.Attestation, error) {
	// We don't index by repo; do a full prefix scan and filter.
	pairs, err := s.meta.ScanPrefix(ctx, []byte("attestation/"), limit)
	if err != nil {
		return nil, err
	}
	var atts []*models.Attestation
	for _, p := range pairs {
		var att models.Attestation
		if err := json.Unmarshal(p.Value, &att); err != nil {
			continue
		}
		if subjectRepo != "" && att.SubjectRepo != subjectRepo {
			continue
		}
		if attType != "" && att.Type != attType {
			continue
		}
		atts = append(atts, &att)
		if limit > 0 && len(atts) >= limit {
			break
		}
	}
	return atts, nil
}

// AttachSBOM stores an SBOM (SPDX or CycloneDX) as an attestation.
func (s *Service) AttachSBOM(ctx context.Context, subjectDigest, subjectRepo string, format models.AttestationType, sbomData []byte) (*models.Attestation, error) {
	att := &models.Attestation{
		SubjectDigest: subjectDigest,
		SubjectRepo:   subjectRepo,
		Type:          format,
		Payload:       sbomData,
		SignedAt:      time.Now(),
	}
	switch format {
	case models.AttestationTypeSBOMSPDX:
		att.PredicateType = "https://spdx.dev/Document"
	case models.AttestationTypeSBOMCycloneDX:
		att.PredicateType = "https://cyclonedx.org/bom"
	}
	if err := s.Attach(ctx, att); err != nil {
		return nil, err
	}
	return att, nil
}

// GetSBOM retrieves the most recent SBOM for a subject.
func (s *Service) GetSBOM(ctx context.Context, subjectDigest string, format models.AttestationType) (*models.Attestation, []byte, error) {
	atts, err := s.GetAttestations(ctx, subjectDigest, format)
	if err != nil {
		return nil, nil, err
	}
	if len(atts) == 0 {
		return nil, nil, fmt.Errorf("no SBOM found for %s", subjectDigest)
	}
	att := atts[len(atts)-1] // most recent
	return att, att.Payload, nil
}

// RecordSLSAProvenance stores a SLSA provenance predicate, optionally signing it.
func (s *Service) RecordSLSAProvenance(ctx context.Context, subjectDigest, subjectRepo string, prov *models.SLSAProvenance, sign bool, signingKey []byte, keyless bool, oidcToken string) (*models.Attestation, error) {
	payload, err := json.Marshal(prov)
	if err != nil {
		return nil, err
	}

	att := &models.Attestation{
		SubjectDigest: subjectDigest,
		SubjectRepo:   subjectRepo,
		Type:          models.AttestationTypeSLSA,
		PredicateType: "https://slsa.dev/provenance/v1",
		Payload:       payload,
		SignedAt:      time.Now(),
	}

	if sign {
		if err := s.signAttestation(ctx, att, signingKey, keyless, oidcToken); err != nil {
			return nil, fmt.Errorf("attestation: sign SLSA: %w", err)
		}
	}

	if err := s.Attach(ctx, att); err != nil {
		return nil, err
	}
	return att, nil
}

// GetSLSAProvenance retrieves the SLSA provenance for a subject.
func (s *Service) GetSLSAProvenance(ctx context.Context, subjectDigest string) (*models.SLSAProvenance, *models.Attestation, error) {
	atts, err := s.GetAttestations(ctx, subjectDigest, models.AttestationTypeSLSA)
	if err != nil {
		return nil, nil, err
	}
	if len(atts) == 0 {
		return nil, nil, fmt.Errorf("no SLSA provenance for %s", subjectDigest)
	}
	att := atts[len(atts)-1]
	var prov models.SLSAProvenance
	if err := json.Unmarshal(att.Payload, &prov); err != nil {
		return nil, nil, err
	}
	return &prov, att, nil
}

// SignArtifact signs an artifact digest using cosign/sigstore.
// If keyless=true, uses Sigstore keyless (Fulcio CA + Rekor transparency log).
func (s *Service) SignArtifact(ctx context.Context, artifactDigest, artifactRepo string, signingKey []byte, keyless bool, oidcToken string) (*models.Attestation, string, []byte, error) {
	att := &models.Attestation{
		SubjectDigest: artifactDigest,
		SubjectRepo:   artifactRepo,
		Type:          models.AttestationTypeCosign,
		PredicateType: "https://cosign.sigstore.dev/attestation/v1",
		SignedAt:      time.Now(),
	}
	if err := s.signAttestation(ctx, att, signingKey, keyless, oidcToken); err != nil {
		return nil, "", nil, err
	}
	if err := s.Attach(ctx, att); err != nil {
		return nil, "", nil, err
	}
	return att, att.TransparencyLogID, att.SigningCert, nil
}

// VerifyArtifact verifies the signature on an artifact.
func (s *Service) VerifyArtifact(ctx context.Context, artifactDigest string, publicKey []byte, checkRekor bool) (bool, string, *models.Attestation, error) {
	atts, err := s.GetAttestations(ctx, artifactDigest, models.AttestationTypeCosign)
	if err != nil || len(atts) == 0 {
		return false, "", nil, fmt.Errorf("no signature found for %s", artifactDigest)
	}
	att := atts[len(atts)-1]
	// In production: verify att.Signature against att.SubjectDigest using publicKey.
	// Here we do a presence check as a placeholder.
	if len(att.Signature) > 0 {
		return true, att.SubjectDigest, att, nil
	}
	return false, "", att, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Signing (production stubs — integrate cosign/sigstore libraries here)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Service) signAttestation(_ context.Context, att *models.Attestation, signingKey []byte, keyless bool, _ string) error {
	if keyless {
		// Production: call cosign.Sign() with Fulcio OIDC flow.
		// For now stamp a placeholder Rekor ID.
		att.TransparencyLogID = fmt.Sprintf("rekor:%s:%d", att.SubjectDigest[:12], time.Now().UnixNano())
		att.Signature = []byte("keyless-signature-placeholder")
		return nil
	}
	if len(signingKey) == 0 {
		return fmt.Errorf("no signing key provided")
	}
	// Production: crypto/ecdsa or cosign.Sign() with provided key.
	att.Signature = []byte(fmt.Sprintf("sig:%x", signingKey[:min(8, len(signingKey))]))
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
