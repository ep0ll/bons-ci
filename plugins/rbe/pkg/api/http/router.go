// Package http wires together all HTTP route handlers that implement the
// OCI Distribution Spec v1 and the RBE-specific REST APIs.
package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"

	"github.com/bons/bons-ci/plugins/rbe/pkg/attestation"
	"github.com/bons/bons-ci/plugins/rbe/pkg/auth"
	"github.com/bons/bons-ci/plugins/rbe/pkg/dag"
	"github.com/bons/bons-ci/plugins/rbe/pkg/mountcache"
	"github.com/bons/bons-ci/plugins/rbe/pkg/observability"
	"github.com/bons/bons-ci/plugins/rbe/pkg/registry"
)

// Services holds references to all domain services.
type Services struct {
	Registry    *registry.Registry
	DAG         *dag.Service
	Cache       *dag.CacheService
	Logs        *dag.LogService
	MountCache  *mountcache.Service
	Attestation *attestation.Service
}

// NewRouter constructs the chi router with all routes and middleware.
func NewRouter(svc Services, authMiddleware *auth.Middleware) http.Handler {
	r := chi.NewRouter()

	// ── Global middleware stack ──────────────────────────────────────────
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(observability.HTTPMiddleware)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-RBE-API-Key", "Docker-Content-Digest", "Docker-Distribution-Api-Version"},
		ExposedHeaders:   []string{"Docker-Content-Digest", "Docker-Distribution-Api-Version", "Docker-Upload-UUID", "Location", "Range", "X-RBE-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(authMiddleware.Handler)

	// ── Health + readiness ───────────────────────────────────────────────
	r.Get("/health", healthHandler)
	r.Get("/ready", healthHandler)
	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observability.MetricsHandler().ServeHTTP(w, r)
	})

	// ── OCI Distribution Spec v1 ─────────────────────────────────────────
	//
	// https://github.com/opencontainers/distribution-spec/blob/main/spec.md
	//
	// Required endpoints:
	//   GET  /v2/                          → API version check
	//   HEAD /v2/<name>/blobs/<digest>     → check blob
	//   GET  /v2/<name>/blobs/<digest>     → pull blob
	//   POST /v2/<name>/blobs/uploads/     → initiate upload
	//   PATCH /v2/<name>/blobs/uploads/<uuid> → chunked upload
	//   PUT  /v2/<name>/blobs/uploads/<uuid>  → complete upload
	//   DELETE /v2/<name>/blobs/uploads/<uuid> → abort upload
	//   DELETE /v2/<name>/blobs/<digest>   → delete blob
	//   GET  /v2/<name>/manifests/<ref>    → pull manifest
	//   HEAD /v2/<name>/manifests/<ref>    → check manifest
	//   PUT  /v2/<name>/manifests/<ref>    → push manifest
	//   DELETE /v2/<name>/manifests/<ref>  → delete manifest
	//   GET  /v2/<name>/tags/list          → list tags
	//   GET  /v2/<name>/referrers/<digest> → list referrers (OCI 1.1)
	//
	h := &ociHandler{svc: svc}
	r.Get("/v2/", h.apiVersionCheck)
	r.Route("/v2/{name}", func(r chi.Router) {
		// Blobs
		r.Head("/blobs/{digest}", h.statBlob)
		r.Get("/blobs/{digest}", h.getBlob)
		r.Delete("/blobs/{digest}", h.deleteBlob)
		// Chunked uploads
		r.Post("/blobs/uploads/", h.initiateUpload)
		r.Get("/blobs/uploads/{uuid}", h.getUploadStatus)
		r.Patch("/blobs/uploads/{uuid}", h.patchUpload)
		r.Put("/blobs/uploads/{uuid}", h.completeUpload)
		r.Delete("/blobs/uploads/{uuid}", h.abortUpload)
		// Manifests
		r.Head("/manifests/{reference}", h.headManifest)
		r.Get("/manifests/{reference}", h.getManifest)
		r.Put("/manifests/{reference}", h.putManifest)
		r.Delete("/manifests/{reference}", h.deleteManifest)
		// Tags
		r.Get("/tags/list", h.listTags)
		// Referrers (OCI 1.1)
		r.Get("/referrers/{digest}", h.getReferrers)
		// Blob list (RBE extension)
		r.Get("/blobs", h.listBlobs)
	})

	// ── DAG API ──────────────────────────────────────────────────────────
	dh := &dagHandler{svc: svc}
	r.Route("/v1/dags", func(r chi.Router) {
		r.Post("/", dh.createDAG)
		r.Get("/", dh.listDAGs)
		r.Get("/{dag_id}", dh.getDAG)
		r.Delete("/{dag_id}", dh.deleteDAG)
		r.Patch("/{dag_id}/status", dh.updateDAGStatus)

		r.Route("/{dag_id}/vertices", func(r chi.Router) {
			r.Post("/", dh.addVertex)
			r.Get("/", dh.listVertices)
			r.Get("/{vertex_id}", dh.getVertex)
			r.Patch("/{vertex_id}/status", dh.updateVertexStatus)
			r.Get("/{vertex_id}/deps", dh.getVertexDeps)
			r.Get("/{vertex_id}/cache", dh.getVertexCache)
		})
	})

	// ── Cache API ─────────────────────────────────────────────────────────
	ch := &cacheHandler{svc: svc}
	r.Route("/v1/cache", func(r chi.Router) {
		r.Post("/check", ch.checkCache)
		r.Post("/store", ch.storeCache)
		r.Get("/{cache_key}", ch.getCacheEntry)
		r.Delete("/{cache_key}", ch.deleteCacheEntry)
		r.Post("/compute-key", ch.computeCacheKey)
		r.Post("/invalidate", ch.invalidateCache)
	})

	// ── Log API ───────────────────────────────────────────────────────────
	lh := &logHandler{svc: svc}
	r.Route("/v1/logs", func(r chi.Router) {
		r.Post("/streams", lh.createLogStream)
		r.Get("/streams/{stream_id}", lh.getLogStream)
		r.Get("/streams/{stream_id}/chunks", lh.getLogs)
		r.Post("/streams/{stream_id}/chunks", lh.uploadChunks)
		r.Post("/streams/{stream_id}/close", lh.closeLogStream)
		// WebSocket real-time tail
		r.Get("/streams/{stream_id}/tail", lh.tailLogs)
		// Vertex-level aggregate
		r.Get("/vertex/{dag_id}/{vertex_id}", lh.getVertexLogs)
		r.Get("/vertex/{dag_id}/{vertex_id}/streams", lh.listVertexStreams)
	})

	// ── Mount Cache API ───────────────────────────────────────────────────
	mh := &mountCacheHandler{svc: svc}
	r.Route("/v1/mountcache", func(r chi.Router) {
		r.Post("/", mh.create)
		r.Get("/", mh.list)
		r.Get("/{id}", mh.get)
		r.Delete("/{id}", mh.delete)
		r.Post("/{id}/lock", mh.lock)
		r.Post("/{id}/unlock", mh.unlock)
		r.Put("/{id}/data", mh.upload)
		r.Get("/{id}/data", mh.download)
		r.Post("/prune", mh.prune)
	})

	// ── Attestation API ───────────────────────────────────────────────────
	ah := &attestationHandler{svc: svc}
	r.Route("/v1/attestations", func(r chi.Router) {
		r.Post("/", ah.attach)
		r.Get("/{subject_digest}", ah.get)
		r.Post("/sbom", ah.attachSBOM)
		r.Get("/{subject_digest}/sbom", ah.getSBOM)
		r.Post("/slsa", ah.recordSLSA)
		r.Get("/{subject_digest}/slsa", ah.getSLSA)
		r.Post("/sign", ah.sign)
		r.Post("/verify", ah.verify)
	})

	// ── Conversion tracking API ───────────────────────────────────────────
	cvh := &conversionHandler{svc: svc}
	r.Route("/v1/conversions", func(r chi.Router) {
		r.Post("/", cvh.record)
		r.Get("/{id}", cvh.get)
		r.Get("/", cvh.list)
		r.Post("/check", cvh.checkExists)
		r.Get("/diff", cvh.getBlobDiff)
	})

	return r
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared utilities
// ─────────────────────────────────────────────────────────────────────────────

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"time":   time.Now().UTC(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
}
