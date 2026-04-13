package http

import (
	"encoding/json"
	"net/http"
	"time"

	pkgdag "github.com/bons/bons-ci/plugins/rbe/pkg/dag"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/observability"
	"github.com/go-chi/chi/v5"
)

type cacheHandler struct{ svc Services }

// POST /v1/cache/check
func (h *cacheHandler) checkCache(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CacheKey  string `json:"cache_key"`
		VertexID  string `json:"vertex_id"`
		WithFiles bool   `json:"with_files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	hit, entry, err := h.svc.Cache.CheckCache(r.Context(), req.CacheKey, req.WithFiles)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	result := "miss"
	if hit {
		result = "hit"
	}
	observability.CacheHits.WithLabelValues(result).Inc()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"hit":   hit,
		"entry": entry,
	})
}

// POST /v1/cache/store
func (h *cacheHandler) storeCache(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CacheKey      string                `json:"cache_key"`
		VertexID      string                `json:"vertex_id"`
		DAGID         string                `json:"dag_id"`
		OutputDigests []string              `json:"output_digests"`
		OutputFiles   []models.FileRef      `json:"output_files"`
		TTLSeconds    int64                 `json:"ttl_seconds"`
		InlineData    []byte                `json:"inline_data"`
		Kind          models.CacheEntryKind `json:"kind"`
		Platform      *models.Platform      `json:"platform"`
		Metadata      map[string]string     `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	entry, err := h.svc.Cache.StoreCache(r.Context(), pkgdag.StoreCacheRequest{
		CacheKey:      req.CacheKey,
		VertexID:      req.VertexID,
		DAGID:         req.DAGID,
		OutputDigests: req.OutputDigests,
		OutputFiles:   req.OutputFiles,
		TTL:           time.Duration(req.TTLSeconds) * time.Second,
		InlineData:    req.InlineData,
		Kind:          req.Kind,
		Platform:      req.Platform,
		Metadata:      req.Metadata,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// GET /v1/cache/{cache_key}
func (h *cacheHandler) getCacheEntry(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "cache_key")
	entry, err := h.svc.Cache.GetCacheEntry(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusNotFound, "CACHE_MISS", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// DELETE /v1/cache/{cache_key}
func (h *cacheHandler) deleteCacheEntry(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "cache_key")
	if err := h.svc.Cache.DeleteCacheEntry(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/cache/compute-key
func (h *cacheHandler) computeCacheKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpDigest        string          `json:"op_digest"`
		InputFileHashes []string        `json:"input_file_hashes"`
		DepCacheKeys    []string        `json:"dep_cache_keys"`
		Platform        models.Platform `json:"platform"`
		Selector        string          `json:"selector"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	key := pkgdag.ComputeCacheKeyFromParts(req.OpDigest, req.InputFileHashes, req.DepCacheKeys, req.Platform, req.Selector)
	writeJSON(w, http.StatusOK, map[string]string{"cache_key": key})
}

// POST /v1/cache/invalidate
func (h *cacheHandler) invalidateCache(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VertexID string `json:"vertex_id"`
		DAGID    string `json:"dag_id"`
		CacheKey string `json:"cache_key"`
		Cascade  bool   `json:"cascade"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	count, err := h.svc.Cache.InvalidateCache(r.Context(), req.VertexID, req.DAGID, req.CacheKey, req.Cascade)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"invalidated_count": count})
}
