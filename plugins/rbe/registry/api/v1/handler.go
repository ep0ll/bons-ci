// Package v1 provides the HTTP API for AccelRegistry.
//
// OCI Distribution Spec (v2):
//   GET  /v2/                                    → API version check
//   HEAD /v2/*name/blobs/{digest}               → blob existence
//   GET  /v2/*name/blobs/{digest}               → blob download
//   POST /v2/*name/blobs/uploads/               → initiate upload
//   PATCH /v2/*name/blobs/uploads/{uuid}        → chunk upload
//   PUT  /v2/*name/blobs/uploads/{uuid}         → finalize upload
//   HEAD /v2/*name/manifests/{reference}        → manifest existence
//   GET  /v2/*name/manifests/{reference}        → manifest download
//   PUT  /v2/*name/manifests/{reference}        → manifest push
//   DELETE /v2/*name/manifests/{reference}      → manifest delete
//   GET  /v2/*name/tags/list                    → tag list
//   GET  /v2/*name/referrers/{digest}           → OCI 1.1 referrers
//
// Accel-specific:
//   GET  /accel/v1/query/{digest}               → query all accel variants
//   POST /accel/v1/pull                         → resolve PullRequest
//   GET  /accel/v1/dag/*name/{digest}           → OCI DAG traversal
//   GET  /accel/v1/metadata/*name/{digest}      → image metadata
//   GET  /accel/v1/stats                        → index statistics
//   GET  /accel/v1/types                        → registered accel types
//   GET  /accel/v1/exists/{digest}              → fast existence check
package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/logger"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/registry"
	pkgerrors "github.com/bons/bons-ci/plugins/rbe/registry/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// Handler holds all HTTP handlers for the AccelRegistry API.
type Handler struct {
	reg *registry.Registry
	log *logger.Logger
}

// New creates a Handler backed by reg.
func New(reg *registry.Registry, log *logger.Logger) *Handler {
	return &Handler{reg: reg, log: log.With(logger.String("component", "api_v1"))}
}

// Router returns a chi.Router with all routes mounted.
// Multi-segment repository names (e.g. "library/node") are handled by
// extracting the repo from the URL path directly, since chi {param} wildcards
// do not capture slashes. Routes use a suffix-based approach:
//   /v2/*nameAndSuffix  captures the entire path after /v2/
// and helper extractors parse repo + tail from it.
func (h *Handler) Router() chi.Router {
	r := chi.NewRouter()

	// OCI version check
	r.Get("/v2/", h.apiVersionCheck)
	r.Get("/v2", h.apiVersionCheck)

	// ── Blob routes ──────────────────────────────────────────────────────
	// Use exact suffix matching. The chi wildcard {name:.+} can't reliably
	// anchor on the suffix, so we use a path-based dispatcher instead.
	r.Head("/v2/*", h.dispatchBlobOrManifest)
	r.Get("/v2/*", h.dispatchBlobOrManifest)
	r.Post("/v2/*", h.dispatchBlobOrManifest)
	r.Patch("/v2/*", h.dispatchBlobOrManifest)
	r.Put("/v2/*", h.dispatchBlobOrManifest)
	r.Delete("/v2/*", h.dispatchBlobOrManifest)

	// ── Accel API ─────────────────────────────────────────────────────────
	r.Get("/accel/v1/query/{digest}", h.queryAccel)
	r.Post("/accel/v1/pull", h.pullAccel)
	r.Get("/accel/v1/dag/*", h.getDAG)
	r.Get("/accel/v1/metadata/*", h.getMetadata)
	r.Get("/accel/v1/stats", h.getStats)
	r.Get("/accel/v1/types", h.getTypes)
	r.Get("/accel/v1/exists/{digest}", h.existsAccel)

	// ── Health ────────────────────────────────────────────────────────────
	r.Get("/healthz", h.healthz)
	r.Get("/readyz", h.readyz)

	return r
}

// ────────────────────────────────────────────────────────────────────────────
// Dispatcher — routes /v2/* requests by path suffix
// ────────────────────────────────────────────────────────────────────────────

// dispatchBlobOrManifest inspects the URL path and dispatches to the correct
// sub-handler. It is the single entry point for all /v2/{name}/... routes.
func (h *Handler) dispatchBlobOrManifest(w http.ResponseWriter, r *http.Request) {
	// chi wildcard: the captured part after /v2/ is in "*"
	tail := chi.URLParam(r, "*")
	// tail = "<repo>/blobs/<digest>"  or  "<repo>/manifests/<ref>"  etc.

	switch {
	case strings.HasSuffix(tail, "/blobs/uploads/") || strings.Contains(tail, "/blobs/uploads/"):
		h.routeUpload(w, r, tail)
	case strings.Contains(tail, "/blobs/"):
		h.routeBlob(w, r, tail)
	case strings.Contains(tail, "/manifests/"):
		h.routeManifest(w, r, tail)
	case strings.HasSuffix(tail, "/tags/list"):
		h.routeTagList(w, r, tail)
	case strings.Contains(tail, "/referrers/"):
		h.routeReferrers(w, r, tail)
	default:
		writeError(w, pkgerrors.ErrNameUnknown.WithDetail(tail))
	}
}

// ── Blob routing ─────────────────────────────────────────────────────────

func (h *Handler) routeBlob(w http.ResponseWriter, r *http.Request, tail string) {
	// tail: "<repo>/blobs/<digest>"
	repo, rest, ok := splitLast(tail, "/blobs/")
	if !ok {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	dgst, err := parseDigest(rest)
	if err != nil {
		writeError(w, pkgerrors.ErrDigestInvalid.WithDetail(err.Error()))
		return
	}
	switch r.Method {
	case http.MethodHead:
		rc, size, err := h.reg.GetBlob(r.Context(), repo, dgst)
		if err != nil {
			writeError(w, err)
			return
		}
		rc.Close()
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.Header().Set("Docker-Content-Digest", dgst.String())
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		rc, size, err := h.reg.GetBlob(r.Context(), repo, dgst)
		if err != nil {
			writeError(w, err)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.Header().Set("Docker-Content-Digest", dgst.String())
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, rc); err != nil {
			h.log.Warn("blob streaming error", logger.Error(err))
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// ── Upload routing ───────────────────────────────────────────────────────

func (h *Handler) routeUpload(w http.ResponseWriter, r *http.Request, tail string) {
	// POST /v2/<repo>/blobs/uploads/           → initiate
	// PATCH /v2/<repo>/blobs/uploads/<uuid>    → chunk
	// PUT   /v2/<repo>/blobs/uploads/<uuid>    → finalize
	const uploadSeg = "/blobs/uploads/"
	idx := strings.Index(tail, uploadSeg)
	if idx < 0 {
		writeError(w, pkgerrors.ErrBlobUploadUnknown)
		return
	}
	repo := tail[:idx]
	uuidPart := tail[idx+len(uploadSeg):]

	switch r.Method {
	case http.MethodPost:
		uuid, err := h.reg.InitiateUpload(r.Context(), repo)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid))
		w.Header().Set("Docker-Upload-UUID", uuid)
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPatch:
		if uuidPart == "" {
			writeError(w, pkgerrors.ErrBlobUploadUnknown)
			return
		}
		contentRange := r.Header.Get("Content-Range")
		var start, end int64
		if contentRange != "" {
			fmt.Sscanf(contentRange, "%d-%d", &start, &end) //nolint:errcheck
		}
		if err := h.reg.ChunkUpload(r.Context(), repo, uuidPart, r.Body, start, end); err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuidPart))
		w.Header().Set("Docker-Upload-UUID", uuidPart)
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPut:
		if uuidPart == "" {
			writeError(w, pkgerrors.ErrBlobUploadUnknown)
			return
		}
		dgstStr := r.URL.Query().Get("digest")
		dgst, err := parseDigest(dgstStr)
		if err != nil {
			writeError(w, pkgerrors.ErrDigestInvalid)
			return
		}
		if r.ContentLength > 0 {
			_ = h.reg.ChunkUpload(r.Context(), repo, uuidPart, r.Body, 0, r.ContentLength)
		}
		if err := h.reg.FinalizeUpload(r.Context(), repo, uuidPart, dgst); err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, dgst))
		w.Header().Set("Docker-Content-Digest", dgst.String())
		w.WriteHeader(http.StatusCreated)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// ── Manifest routing ─────────────────────────────────────────────────────

func (h *Handler) routeManifest(w http.ResponseWriter, r *http.Request, tail string) {
	// tail: "<repo>/manifests/<reference>"
	repo, ref, ok := splitLast(tail, "/manifests/")
	if !ok || ref == "" {
		writeError(w, pkgerrors.ErrManifestInvalid)
		return
	}
	switch r.Method {
	case http.MethodHead:
		desc, _, err := h.reg.GetManifest(r.Context(), repo, ref)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", desc.MediaType)
		w.Header().Set("Docker-Content-Digest", desc.Digest.String())
		w.Header().Set("Content-Length", strconv.FormatInt(desc.Size, 10))
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		desc, raw, err := h.reg.GetManifest(r.Context(), repo, ref)
		if err != nil {
			writeError(w, err)
			return
		}
		mt := desc.MediaType
		if mt == "" {
			mt = ocispec.MediaTypeImageManifest
		}
		w.Header().Set("Content-Type", mt)
		w.Header().Set("Docker-Content-Digest", desc.Digest.String())
		w.Header().Set("Content-Length", strconv.FormatInt(int64(len(raw)), 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)

	case http.MethodPut:
		mediaType := r.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = ocispec.MediaTypeImageManifest
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
		if err != nil {
			writeError(w, pkgerrors.Wrap(pkgerrors.CodeManifestInvalid, 400, "reading body", err))
			return
		}
		dgst, err := h.reg.PutManifest(r.Context(), repo, ref, mediaType, body)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Docker-Content-Digest", dgst.String())
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, dgst))
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		dgst, err := parseDigest(ref)
		if err != nil {
			// ref is a tag — look it up
			desc, lookupErr := func() (ocispec.Descriptor, error) {
				d, _, e := h.reg.GetManifest(r.Context(), repo, ref)
				return d, e
			}()
			if lookupErr != nil {
				writeError(w, lookupErr)
				return
			}
			dgst = desc.Digest
		}
		if err := h.reg.DeleteManifest(r.Context(), repo, dgst); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// ── Tag list routing ─────────────────────────────────────────────────────

func (h *Handler) routeTagList(w http.ResponseWriter, r *http.Request, tail string) {
	repo := strings.TrimSuffix(tail, "/tags/list")
	last := r.URL.Query().Get("last")
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	tags, err := h.reg.GetTags(r.Context(), repo, last, n)
	if err != nil {
		writeError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name": repo,
		"tags": tags,
	})
}

// ── Referrers routing ────────────────────────────────────────────────────

func (h *Handler) routeReferrers(w http.ResponseWriter, r *http.Request, tail string) {
	repo, dgstStr, ok := splitLast(tail, "/referrers/")
	if !ok {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	dgst, err := parseDigest(dgstStr)
	if err != nil {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	artifactType := r.URL.Query().Get("artifactType")
	descs, err := h.reg.GetReferrers(r.Context(), repo, dgst, artifactType)
	if err != nil {
		writeError(w, err)
		return
	}
	if descs == nil {
		descs = []ocispec.Descriptor{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     descs,
	})
}

// ────────────────────────────────────────────────────────────────────────────
// OCI version check
// ────────────────────────────────────────────────────────────────────────────

func (h *Handler) apiVersionCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("OCI-Distribution-Spec-Version", "1.1.0")
	w.WriteHeader(http.StatusOK)
}

// ────────────────────────────────────────────────────────────────────────────
// Accel API handlers
// ────────────────────────────────────────────────────────────────────────────

// GET /accel/v1/query/{digest}
func (h *Handler) queryAccel(w http.ResponseWriter, r *http.Request) {
	dgst, err := parseDigest(chi.URLParam(r, "digest"))
	if err != nil {
		writeError(w, pkgerrors.ErrDigestInvalid.WithDetail(err.Error()))
		return
	}
	result, err := h.reg.QueryAccel(r.Context(), dgst)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if !result.Found {
		status = http.StatusNotFound
	}
	writeJSON(w, status, result)
}

// POST /accel/v1/pull
func (h *Handler) pullAccel(w http.ResponseWriter, r *http.Request) {
	var req types.PullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, pkgerrors.Wrap(pkgerrors.CodeManifestInvalid, 400, "decoding pull request", err))
		return
	}
	if req.SourceDigest == "" {
		writeError(w, pkgerrors.New(pkgerrors.CodeDigestInvalid, 400, "sourceDigest is required"))
		return
	}
	result, err := h.reg.PullAccel(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /accel/v1/dag/*  — tail is "<name>/<digest>"
func (h *Handler) getDAG(w http.ResponseWriter, r *http.Request) {
	tail := chi.URLParam(r, "*")
	// last segment is the digest, everything before is the repo
	lastSlash := strings.LastIndex(tail, "/")
	if lastSlash < 0 {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	repo := tail[:lastSlash]
	dgst, err := parseDigest(tail[lastSlash+1:])
	if err != nil {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	result, err := h.reg.GetDAG(r.Context(), repo, dgst)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /accel/v1/metadata/* — tail is "<name>/<digest>"
func (h *Handler) getMetadata(w http.ResponseWriter, r *http.Request) {
	tail := chi.URLParam(r, "*")
	lastSlash := strings.LastIndex(tail, "/")
	if lastSlash < 0 {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	repo := tail[:lastSlash]
	dgst, err := parseDigest(tail[lastSlash+1:])
	if err != nil {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	meta, err := h.reg.GetImageMetadata(r.Context(), repo, dgst)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// GET /accel/v1/stats
func (h *Handler) getStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.reg.IndexStats())
}

// GET /accel/v1/types
func (h *Handler) getTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"accelTypes": types.KnownAccelTypes,
	})
}

// GET /accel/v1/exists/{digest}
func (h *Handler) existsAccel(w http.ResponseWriter, r *http.Request) {
	dgst, err := parseDigest(chi.URLParam(r, "digest"))
	if err != nil {
		writeError(w, pkgerrors.ErrDigestInvalid)
		return
	}
	result, err := h.reg.QueryAccel(r.Context(), dgst)
	if err != nil {
		writeError(w, err)
		return
	}
	if !result.Found {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"found": false, "sourceDigest": dgst,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"found":          true,
		"sourceDigest":   dgst,
		"totalVariants":  result.TotalVariants,
		"supportedTypes": result.SupportedTypes,
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Health
// ────────────────────────────────────────────────────────────────────────────

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// ────────────────────────────────────────────────────────────────────────────
// Error & JSON helpers
// ────────────────────────────────────────────────────────────────────────────

type ociError struct {
	Errors []ociErrorDetail `json:"errors"`
}
type ociErrorDetail struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Detail  interface{} `json:"detail,omitempty"`
}

func writeError(w http.ResponseWriter, err error) {
	status := pkgerrors.HTTPStatus(err)
	var code, msg string
	var detail interface{}
	if re, ok := err.(*pkgerrors.RegistryError); ok {
		code = string(re.Code)
		msg = re.Message
		detail = re.Detail
	} else {
		code = "INTERNAL_ERROR"
		msg = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ociError{
		Errors: []ociErrorDetail{{Code: code, Message: msg, Detail: detail}},
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ────────────────────────────────────────────────────────────────────────────
// URL path helpers
// ────────────────────────────────────────────────────────────────────────────

// splitLast splits s on the last occurrence of sep, returning (before, after, true).
// Returns ("", "", false) if sep is not found.
func splitLast(s, sep string) (string, string, bool) {
	idx := strings.Index(s, sep)
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], s[idx+len(sep):], true
}

func parseDigest(s string) (digest.Digest, error) {
	if s == "" {
		return "", fmt.Errorf("empty digest")
	}
	return digest.Parse(s)
}

// RequestID generates a unique request identifier for tracing.
func RequestID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
