package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/observability"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type logHandler struct{ svc Services }

// POST /v1/logs/streams
func (h *logHandler) createLogStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VertexID string            `json:"vertex_id"`
		DAGID    string            `json:"dag_id"`
		FDType   models.FDType     `json:"fd_type"`
		FDNum    int               `json:"fd_num"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	stream, err := h.svc.Logs.CreateLogStream(r.Context(), req.DAGID, req.VertexID, req.FDType, req.FDNum, req.Metadata)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, stream)
}

// GET /v1/logs/streams/{stream_id}
func (h *logHandler) getLogStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stream_id")
	stream, err := h.svc.Logs.GetLogStream(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "STREAM_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stream)
}

// GET /v1/logs/streams/{stream_id}/chunks  — buffered fetch
func (h *logHandler) getLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stream_id")
	fromSeq, _ := strconv.ParseInt(r.URL.Query().Get("from_seq"), 10, 64)
	toSeq, _ := strconv.ParseInt(r.URL.Query().Get("to_seq"), 10, 64)
	maxChunks, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	chunks, hasMore, nextSeq, err := h.svc.Logs.GetLogs(r.Context(), id, fromSeq, toSeq, maxChunks)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chunks":   chunks,
		"has_more": hasMore,
		"next_seq": nextSeq,
	})
}

// POST /v1/logs/streams/{stream_id}/chunks  — ingest
func (h *logHandler) uploadChunks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stream_id")
	var req struct {
		Sequence  int64         `json:"sequence"`
		Data      []byte        `json:"data"`
		Timestamp time.Time     `json:"timestamp"`
		FDType    models.FDType `json:"fd_type"`
		FDNum     int           `json:"fd_num"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.Timestamp.IsZero() {
		req.Timestamp = time.Now()
	}
	if err := h.svc.Logs.UploadChunk(r.Context(), id, req.Sequence, req.Data, req.Timestamp, req.FDType, req.FDNum); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	observability.LogChunksIngested.WithLabelValues(strconv.Itoa(int(req.FDType))).Inc()
	observability.LogBytesIngested.Add(float64(len(req.Data)))
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// POST /v1/logs/streams/{stream_id}/close
func (h *logHandler) closeLogStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stream_id")
	stream, err := h.svc.Logs.CloseLogStream(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stream)
}

// GET /v1/logs/streams/{stream_id}/tail  — WebSocket live tail
func (h *logHandler) tailLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stream_id")
	fromSeq, _ := strconv.ParseInt(r.URL.Query().Get("from_seq"), 10, 64)
	follow := r.URL.Query().Get("follow") != "false"

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch, err := h.svc.Logs.TailLogs(r.Context(), id, fromSeq, follow)
	if err != nil {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1011, err.Error()))
		return
	}

	for chunk := range ch {
		data, _ := json.Marshal(chunk)
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
	conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stream closed"))
}

// GET /v1/logs/vertex/{dag_id}/{vertex_id}
func (h *logHandler) getVertexLogs(w http.ResponseWriter, r *http.Request) {
	dagID := chi.URLParam(r, "dag_id")
	vertexID := chi.URLParam(r, "vertex_id")
	interleaved := r.URL.Query().Get("interleaved") == "true"

	// Parse FD filter
	var fds []models.FDType
	for _, fdStr := range r.URL.Query()["fd"] {
		fd, _ := strconv.Atoi(fdStr)
		fds = append(fds, models.FDType(fd))
	}

	chunks, streams, err := h.svc.Logs.GetVertexLogs(r.Context(), dagID, vertexID, fds, interleaved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chunks":  chunks,
		"streams": streams,
		"vertex":  vertexID,
		"dag":     dagID,
	})
}

// GET /v1/logs/vertex/{dag_id}/{vertex_id}/streams
func (h *logHandler) listVertexStreams(w http.ResponseWriter, r *http.Request) {
	dagID := chi.URLParam(r, "dag_id")
	vertexID := chi.URLParam(r, "vertex_id")
	streams, err := h.svc.Logs.ListLogStreams(r.Context(), dagID, vertexID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"streams": streams, "count": len(streams)})
}
