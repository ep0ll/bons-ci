package http

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/go-chi/chi/v5"
)

type mountCacheHandler struct{ svc Services }

// POST /v1/mountcache
func (h *mountCacheHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserKey          string                  `json:"user_key"`
		Scope            string                  `json:"scope"`
		PlatformSpecific bool                    `json:"platform_specific"`
		Platform         *models.Platform        `json:"platform"`
		Sharing          models.CacheSharingMode `json:"sharing"`
		Labels           map[string]string       `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	cache, err := h.svc.MountCache.Create(r.Context(), req.UserKey, req.Scope, req.PlatformSpecific, req.Platform, req.Sharing, req.Labels)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, cache)
}

// GET /v1/mountcache
func (h *mountCacheHandler) list(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var platform *models.Platform
	if os := r.URL.Query().Get("os"); os != "" {
		platform = &models.Platform{OS: os, Arch: r.URL.Query().Get("arch")}
	}
	caches, err := h.svc.MountCache.List(r.Context(), scope, platform, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"caches": caches, "count": len(caches)})
}

// GET /v1/mountcache/{id}
func (h *mountCacheHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cache, err := h.svc.MountCache.Get(r.Context(), id, "", "", nil)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cache)
}

// DELETE /v1/mountcache/{id}
func (h *mountCacheHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.MountCache.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/mountcache/{id}/lock
func (h *mountCacheHandler) lock(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Owner      string `json:"owner"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl == 0 {
		ttl = 60 * time.Second
	}
	acquired, cache, err := h.svc.MountCache.Lock(r.Context(), id, req.Owner, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"acquired": acquired, "cache": cache})
}

// POST /v1/mountcache/{id}/unlock
func (h *mountCacheHandler) unlock(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Owner string `json:"owner"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := h.svc.MountCache.Unlock(r.Context(), id, req.Owner); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /v1/mountcache/{id}/data
func (h *mountCacheHandler) upload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	digest, err := h.svc.MountCache.Upload(r.Context(), id, r.Body, r.ContentLength)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"blob_digest": digest})
}

// GET /v1/mountcache/{id}/data
func (h *mountCacheHandler) download(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	length, _ := strconv.ParseInt(r.URL.Query().Get("length"), 10, 64)
	rc, size, err := h.svc.MountCache.Download(r.Context(), id, offset, length)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, rc)
}

// POST /v1/mountcache/prune
func (h *mountCacheHandler) prune(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope            string `json:"scope"`
		All              bool   `json:"all"`
		OlderThanSeconds int64  `json:"older_than_seconds"`
		KeepBytes        int64  `json:"keep_bytes"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	pruned, freed, err := h.svc.MountCache.Prune(r.Context(), req.Scope, req.All, time.Duration(req.OlderThanSeconds)*time.Second, req.KeepBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"pruned_count": pruned, "freed_bytes": freed})
}
