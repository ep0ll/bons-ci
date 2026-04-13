package http

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/observability"
	"github.com/go-chi/chi/v5"
)

type dagHandler struct{ svc Services }

// POST /v1/dags
func (h *dagHandler) createDAG(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BuildID     string            `json:"build_id"`
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Description string            `json:"description"`
		Platform    *models.Platform  `json:"platform"`
		CreatedBy   string            `json:"created_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	dag, err := h.svc.DAG.CreateDAG(r.Context(), req.BuildID, req.Name, req.Labels, req.Platform, req.Description, req.CreatedBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	observability.DAGsCreated.Inc()
	writeJSON(w, http.StatusCreated, dag)
}

// GET /v1/dags
func (h *dagHandler) listDAGs(w http.ResponseWriter, r *http.Request) {
	buildID := r.URL.Query().Get("build_id")
	status := models.DAGStatus(r.URL.Query().Get("status"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	dags, err := h.svc.DAG.ListDAGs(r.Context(), buildID, status, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"dags": dags, "count": len(dags)})
}

// GET /v1/dags/{dag_id}
func (h *dagHandler) getDAG(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "dag_id")
	dag, err := h.svc.DAG.GetDAG(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "DAG_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dag)
}

// DELETE /v1/dags/{dag_id}
func (h *dagHandler) deleteDAG(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "dag_id")
	if err := h.svc.DAG.DeleteDAG(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PATCH /v1/dags/{dag_id}/status
func (h *dagHandler) updateDAGStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "dag_id")
	var req struct {
		Status models.DAGStatus `json:"status"`
		Error  string           `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	dag, err := h.svc.DAG.UpdateDAGStatus(r.Context(), id, req.Status, req.Error)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dag)
}

// POST /v1/dags/{dag_id}/vertices
func (h *dagHandler) addVertex(w http.ResponseWriter, r *http.Request) {
	dagID := chi.URLParam(r, "dag_id")
	var v models.Vertex
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	v.DAGID = dagID
	result, err := h.svc.DAG.AddVertex(r.Context(), &v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	observability.VerticesTotal.WithLabelValues(string(result.Status), result.OpType).Inc()
	writeJSON(w, http.StatusCreated, result)
}

// GET /v1/dags/{dag_id}/vertices
func (h *dagHandler) listVertices(w http.ResponseWriter, r *http.Request) {
	dagID := chi.URLParam(r, "dag_id")
	status := models.VertexStatus(r.URL.Query().Get("status"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	vertices, err := h.svc.DAG.ListVertices(r.Context(), dagID, status, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"vertices": vertices, "count": len(vertices)})
}

// GET /v1/dags/{dag_id}/vertices/{vertex_id}
func (h *dagHandler) getVertex(w http.ResponseWriter, r *http.Request) {
	dagID, vertexID := chi.URLParam(r, "dag_id"), chi.URLParam(r, "vertex_id")
	v, err := h.svc.DAG.GetVertex(r.Context(), dagID, vertexID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VERTEX_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// PATCH /v1/dags/{dag_id}/vertices/{vertex_id}/status
func (h *dagHandler) updateVertexStatus(w http.ResponseWriter, r *http.Request) {
	dagID, vertexID := chi.URLParam(r, "dag_id"), chi.URLParam(r, "vertex_id")
	var req struct {
		Status       models.VertexStatus   `json:"status"`
		Error        string                `json:"error"`
		ErrorDetails string                `json:"error_details"`
		OutputFiles  []models.FileRef      `json:"output_files"`
		Resources    *models.ResourceUsage `json:"resource_usage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	v, err := h.svc.DAG.UpdateVertexStatus(r.Context(), dagID, vertexID, req.Status, req.Error, req.ErrorDetails, req.OutputFiles, req.Resources)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if v.Resources != nil && v.CompletedAt != nil && v.StartedAt != nil {
		dur := v.CompletedAt.Sub(*v.StartedAt).Seconds()
		observability.VertexDuration.WithLabelValues(v.OpType, string(v.Status)).Observe(dur)
	}
	observability.VerticesTotal.WithLabelValues(string(req.Status), v.OpType).Inc()
	writeJSON(w, http.StatusOK, v)
}

// GET /v1/dags/{dag_id}/vertices/{vertex_id}/deps
func (h *dagHandler) getVertexDeps(w http.ResponseWriter, r *http.Request) {
	dagID, vertexID := chi.URLParam(r, "dag_id"), chi.URLParam(r, "vertex_id")
	maxDepth, _ := strconv.Atoi(r.URL.Query().Get("max_depth"))
	tree, err := h.svc.DAG.GetVertexDependencyTree(r.Context(), dagID, vertexID, maxDepth)
	if err != nil {
		writeError(w, http.StatusNotFound, "VERTEX_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// GET /v1/dags/{dag_id}/vertices/{vertex_id}/cache
func (h *dagHandler) getVertexCache(w http.ResponseWriter, r *http.Request) {
	dagID, vertexID := chi.URLParam(r, "dag_id"), chi.URLParam(r, "vertex_id")
	entries, err := h.svc.Cache.ListCacheEntries(r.Context(), dagID, vertexID, nil, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries, "count": len(entries)})
}
