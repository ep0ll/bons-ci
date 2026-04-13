package http

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/observability"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
	"github.com/go-chi/chi/v5"
)

type ociHandler struct{ svc Services }

// GET /v2/
func (h *ociHandler) apiVersionCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// HEAD /v2/<name>/blobs/<digest>
func (h *ociHandler) statBlob(w http.ResponseWriter, r *http.Request) {
	repo, digest := chi.URLParam(r, "name"), chi.URLParam(r, "digest")
	desc, err := h.svc.Registry.StatBlob(r.Context(), repo, digest)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(desc.Size, 10))
	w.Header().Set("Docker-Content-Digest", desc.Digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	observability.BlobDownloads.WithLabelValues(repo, "head").Inc()
}

// GET /v2/<name>/blobs/<digest>
func (h *ociHandler) getBlob(w http.ResponseWriter, r *http.Request) {
	repo, digest := chi.URLParam(r, "name"), chi.URLParam(r, "digest")
	desc, err := h.svc.Registry.StatBlob(r.Context(), repo, digest)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	opts := storage.GetOptions{}
	// Handle Range requests (resumable downloads)
	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		offset, length, err := parseRange(rangeHdr, desc.Size)
		if err != nil {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		opts.Offset = offset
		opts.Length = length
	}

	// Get actual blob from store via registry abstraction
	// (registry.Registry wraps the store but exposes blobs via StatBlob only;
	//  for data we go via the store directly through a helper).
	rc, size, err := h.svc.Registry.GetBlobReader(r.Context(), digest, opts)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Docker-Content-Digest", desc.Digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	if opts.Offset > 0 || opts.Length > 0 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", opts.Offset, opts.Offset+size-1, desc.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(desc.Size, 10))
		w.WriteHeader(http.StatusOK)
	}
	written, _ := io.Copy(w, rc)
	observability.BlobDownloads.WithLabelValues(repo, "get").Inc()
	observability.BlobSize.WithLabelValues("download").Observe(float64(written))
}

// DELETE /v2/<name>/blobs/<digest>
func (h *ociHandler) deleteBlob(w http.ResponseWriter, r *http.Request) {
	repo, digest := chi.URLParam(r, "name"), chi.URLParam(r, "digest")
	if err := h.svc.Registry.DeleteBlob(r.Context(), repo, digest); err != nil {
		writeOCIError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// POST /v2/<name>/blobs/uploads/
func (h *ociHandler) initiateUpload(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "name")

	// Monolithic PUT support: if digest query param present, do a single-step upload.
	if digest := r.URL.Query().Get("digest"); digest != "" {
		h.handleMonolithicUpload(w, r, repo, digest)
		return
	}

	sess, err := h.svc.Registry.InitiateUpload(r.Context(), repo)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, sess.UUID))
	w.Header().Set("Docker-Upload-UUID", sess.UUID)
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
	observability.BlobUploads.WithLabelValues(repo, "initiated").Inc()
}

func (h *ociHandler) handleMonolithicUpload(w http.ResponseWriter, r *http.Request, repo, digest string) {
	sess, err := h.svc.Registry.InitiateUpload(r.Context(), repo)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 32*1024*1024*1024))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	if _, err := h.svc.Registry.UploadChunk(r.Context(), repo, sess.UUID, 1, data, int64(len(data))); err != nil {
		writeOCIError(w, err)
		return
	}
	desc, err := h.svc.Registry.CompleteUpload(r.Context(), repo, sess.UUID, digest, []storage.Part{{Number: 1, Size: int64(len(data))}})
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Docker-Content-Digest", desc.Digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, desc.Digest))
	w.WriteHeader(http.StatusCreated)
	observability.BlobUploads.WithLabelValues(repo, "monolithic").Inc()
	observability.BlobSize.WithLabelValues("upload").Observe(float64(len(data)))
}

// GET /v2/<name>/blobs/uploads/<uuid>
func (h *ociHandler) getUploadStatus(w http.ResponseWriter, r *http.Request) {
	repo, uuid := chi.URLParam(r, "name"), chi.URLParam(r, "uuid")
	sess, err := h.svc.Registry.GetUploadSession(r.Context(), repo, uuid)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid))
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.Header().Set("Range", fmt.Sprintf("0-%d", sess.Offset))
	w.WriteHeader(http.StatusNoContent)
}

// PATCH /v2/<name>/blobs/uploads/<uuid>
func (h *ociHandler) patchUpload(w http.ResponseWriter, r *http.Request) {
	repo, uuid := chi.URLParam(r, "name"), chi.URLParam(r, "uuid")
	sess, err := h.svc.Registry.GetUploadSession(r.Context(), repo, uuid)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	// Determine part number from current offset.
	partNum := int(sess.Offset/defaultPartSize) + 1

	data, err := io.ReadAll(io.LimitReader(r.Body, defaultPartSize+1024))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	if _, err := h.svc.Registry.UploadChunk(r.Context(), repo, uuid, partNum, data, int64(len(data))); err != nil {
		writeOCIError(w, err)
		return
	}
	updatedSess, _ := h.svc.Registry.GetUploadSession(r.Context(), repo, uuid)
	newOffset := sess.Offset + int64(len(data))
	if updatedSess != nil {
		newOffset = updatedSess.Offset
	}
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid))
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.Header().Set("Range", fmt.Sprintf("0-%d", newOffset-1))
	w.WriteHeader(http.StatusAccepted)
}

// PUT /v2/<name>/blobs/uploads/<uuid>
func (h *ociHandler) completeUpload(w http.ResponseWriter, r *http.Request) {
	repo, uuid := chi.URLParam(r, "name"), chi.URLParam(r, "uuid")
	digest := r.URL.Query().Get("digest")
	if digest == "" {
		writeError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query parameter required")
		return
	}

	sess, err := h.svc.Registry.GetUploadSession(r.Context(), repo, uuid)
	if err != nil {
		writeOCIError(w, err)
		return
	}

	// Any remaining body data is the final part.
	if r.ContentLength > 0 {
		data, _ := io.ReadAll(r.Body)
		if len(data) > 0 {
			partNum := int(sess.Offset/defaultPartSize) + 1
			h.svc.Registry.UploadChunk(r.Context(), repo, uuid, partNum, data, int64(len(data))) //nolint:errcheck
		}
	}

	// Build part list from session status.
	status, err := h.svc.Registry.GetUploadParts(r.Context(), uuid)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	desc, err := h.svc.Registry.CompleteUpload(r.Context(), repo, uuid, digest, status)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Docker-Content-Digest", desc.Digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, desc.Digest))
	w.WriteHeader(http.StatusCreated)
	observability.BlobUploads.WithLabelValues(repo, "complete").Inc()
}

// DELETE /v2/<name>/blobs/uploads/<uuid>
func (h *ociHandler) abortUpload(w http.ResponseWriter, r *http.Request) {
	repo, uuid := chi.URLParam(r, "name"), chi.URLParam(r, "uuid")
	_ = h.svc.Registry.AbortUpload(r.Context(), repo, uuid)
	w.WriteHeader(http.StatusNoContent)
}

// HEAD /v2/<name>/manifests/<reference>
func (h *ociHandler) headManifest(w http.ResponseWriter, r *http.Request) {
	repo, ref := chi.URLParam(r, "name"), chi.URLParam(r, "reference")
	m, err := h.svc.Registry.GetManifest(r.Context(), repo, ref)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("Content-Type", m.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.WriteHeader(http.StatusOK)
	observability.ManifestOps.WithLabelValues("head", repo, "ok").Inc()
}

// GET /v2/<name>/manifests/<reference>
func (h *ociHandler) getManifest(w http.ResponseWriter, r *http.Request) {
	repo, ref := chi.URLParam(r, "name"), chi.URLParam(r, "reference")

	// Accept header negotiation
	accept := r.Header.Get("Accept")
	_ = accept // could filter by format

	m, err := h.svc.Registry.GetManifest(r.Context(), repo, ref)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	raw, err := h.svc.Registry.GetRawManifest(r.Context(), repo, ref)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("Content-Type", m.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(raw)), 10))
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
	observability.ManifestOps.WithLabelValues("get", repo, "ok").Inc()
}

// PUT /v2/<name>/manifests/<reference>
func (h *ociHandler) putManifest(w http.ResponseWriter, r *http.Request) {
	repo, ref := chi.URLParam(r, "name"), chi.URLParam(r, "reference")
	ct := r.Header.Get("Content-Type")
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeOCIError(w, err)
		return
	}
	m, err := h.svc.Registry.PutManifest(r.Context(), repo, ref, raw, ct)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, m.Digest))
	w.WriteHeader(http.StatusCreated)
	observability.ManifestOps.WithLabelValues("put", repo, "ok").Inc()
}

// DELETE /v2/<name>/manifests/<reference>
func (h *ociHandler) deleteManifest(w http.ResponseWriter, r *http.Request) {
	repo, ref := chi.URLParam(r, "name"), chi.URLParam(r, "reference")
	if err := h.svc.Registry.DeleteManifest(r.Context(), repo, ref); err != nil {
		writeOCIError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	observability.ManifestOps.WithLabelValues("delete", repo, "ok").Inc()
}

// GET /v2/<name>/tags/list
func (h *ociHandler) listTags(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "name")
	tags, err := h.svc.Registry.ListTags(r.Context(), repo)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name": repo,
		"tags": tags,
	})
}

// GET /v2/<name>/referrers/<digest>
func (h *ociHandler) getReferrers(w http.ResponseWriter, r *http.Request) {
	repo, digest := chi.URLParam(r, "name"), chi.URLParam(r, "digest")
	artifactType := r.URL.Query().Get("artifactType")
	refs, err := h.svc.Registry.GetReferrers(r.Context(), repo, digest, artifactType)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	// OCI 1.1 referrers response
	manifests := make([]map[string]interface{}, 0, len(refs))
	for _, m := range refs {
		manifests = append(manifests, map[string]interface{}{
			"digest":       m.Digest,
			"mediaType":    m.MediaType,
			"size":         m.Size,
			"artifactType": m.ArtifactType,
			"annotations":  m.Annotations,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     manifests,
	})
}

// GET /v2/<name>/blobs (RBE extension: list all blobs for a repo/manifest)
func (h *ociHandler) listBlobs(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "name")
	manifestDigest := r.URL.Query().Get("manifest")
	blobs, err := h.svc.Registry.ListBlobs(r.Context(), repo, manifestDigest, 0)
	if err != nil {
		writeOCIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"repository": repo,
		"blobs":      blobs,
		"count":      len(blobs),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

const defaultPartSize = 64 * 1024 * 1024 // 64 MiB

func writeOCIError(w http.ResponseWriter, err error) {
	var rbeErr *errors.RBEError
	if errors.As(err, &rbeErr) {
		writeJSON(w, rbeErr.HTTPStatus, map[string]interface{}{
			"errors": []map[string]interface{}{{"code": rbeErr.Code, "message": rbeErr.Message}},
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
		"errors": []map[string]interface{}{{"code": "INTERNAL", "message": err.Error()}},
	})
}

// parseRange parses an HTTP Range header: "bytes=<start>-<end>"
func parseRange(rangeHdr string, total int64) (offset, length int64, err error) {
	rangeHdr = strings.TrimPrefix(rangeHdr, "bytes=")
	parts := strings.SplitN(rangeHdr, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	var end int64
	if parts[1] == "" {
		end = total - 1
	} else {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, 0, err
		}
	}
	if start > end || end >= total {
		return 0, 0, errors.ErrRangeNotSatisfiable
	}
	return start, end - start + 1, nil
}
