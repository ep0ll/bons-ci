package http

import (
	"encoding/json"
	"net/http"

	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/go-chi/chi/v5"
)

type attestationHandler struct{ svc Services }

func (h *attestationHandler) attach(w http.ResponseWriter, r *http.Request) {
	var att models.Attestation
	if err := json.NewDecoder(r.Body).Decode(&att); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if err := h.svc.Attestation.Attach(r.Context(), &att); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, att)
}

func (h *attestationHandler) get(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "subject_digest")
	attType := models.AttestationType(r.URL.Query().Get("type"))
	atts, err := h.svc.Attestation.GetAttestations(r.Context(), digest, attType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"attestations": atts})
}

func (h *attestationHandler) attachSBOM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubjectDigest string                 `json:"subject_digest"`
		SubjectRepo   string                 `json:"subject_repo"`
		Format        models.AttestationType `json:"format"`
		SBOMData      []byte                 `json:"sbom_data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	att, err := h.svc.Attestation.AttachSBOM(r.Context(), req.SubjectDigest, req.SubjectRepo, req.Format, req.SBOMData)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, att)
}

func (h *attestationHandler) getSBOM(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "subject_digest")
	format := models.AttestationType(r.URL.Query().Get("format"))
	att, data, err := h.svc.Attestation.GetSBOM(r.Context(), digest, format)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"attestation": att, "sbom_data": data})
}

func (h *attestationHandler) recordSLSA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubjectDigest string                `json:"subject_digest"`
		SubjectRepo   string                `json:"subject_repo"`
		Provenance    models.SLSAProvenance `json:"provenance"`
		Sign          bool                  `json:"sign"`
		SigningKey    []byte                `json:"signing_key"`
		Keyless       bool                  `json:"keyless"`
		OIDCToken     string                `json:"oidc_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	att, err := h.svc.Attestation.RecordSLSAProvenance(r.Context(), req.SubjectDigest, req.SubjectRepo, &req.Provenance, req.Sign, req.SigningKey, req.Keyless, req.OIDCToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, att)
}

func (h *attestationHandler) getSLSA(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "subject_digest")
	prov, att, err := h.svc.Attestation.GetSLSAProvenance(r.Context(), digest)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"provenance": prov, "attestation": att})
}

func (h *attestationHandler) sign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArtifactDigest string `json:"artifact_digest"`
		ArtifactRepo   string `json:"artifact_repo"`
		SigningKey     []byte `json:"signing_key"`
		Keyless        bool   `json:"keyless"`
		OIDCToken      string `json:"oidc_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	att, rekorID, cert, err := h.svc.Attestation.SignArtifact(r.Context(), req.ArtifactDigest, req.ArtifactRepo, req.SigningKey, req.Keyless, req.OIDCToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"attestation": att, "rekor_log_id": rekorID, "certificate": cert})
}

func (h *attestationHandler) verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArtifactDigest string `json:"artifact_digest"`
		ArtifactRepo   string `json:"artifact_repo"`
		PublicKey      []byte `json:"public_key"`
		CheckRekor     bool   `json:"check_rekor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	valid, signer, att, err := h.svc.Attestation.VerifyArtifact(r.Context(), req.ArtifactDigest, req.PublicKey, req.CheckRekor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"valid": valid, "signer_identity": signer, "attestation": att})
}

// ── Conversion tracking handler ───────────────────────────────────────────────
type conversionHandler struct{ svc Services }

func (h *conversionHandler) record(w http.ResponseWriter, r *http.Request) {
	var rec models.ConversionRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if err := h.svc.Registry.RecordConversion(r.Context(), &rec); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (h *conversionHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.svc.Registry.GetConversion(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *conversionHandler) list(w http.ResponseWriter, r *http.Request) {
	sourceDigest := r.URL.Query().Get("source_digest")
	targetFormat := models.ImageFormat(r.URL.Query().Get("target_format"))
	recs, err := h.svc.Registry.ListConversions(r.Context(), sourceDigest, targetFormat)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"records": recs, "count": len(recs)})
}

func (h *conversionHandler) checkExists(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceDigest string             `json:"source_digest"`
		TargetFormat models.ImageFormat `json:"target_format"`
		VerifyBlobs  bool               `json:"verify_blobs"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	exists, rec, missing := h.svc.Registry.CheckConversionExists(r.Context(), req.SourceDigest, req.TargetFormat, req.VerifyBlobs)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"exists":          exists,
		"record":          rec,
		"missing_blobs":   missing,
		"fully_available": exists && len(missing) == 0,
	})
}

func (h *conversionHandler) getBlobDiff(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	dst := r.URL.Query().Get("dst")
	added, removed, shared, err := h.svc.Registry.ConversionBlobDiff(r.Context(), src, dst)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"added": added, "removed": removed, "shared": shared})
}
