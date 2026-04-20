package reactdag

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// BuildServer — HTTP API for the build engine
// ---------------------------------------------------------------------------

// BuildServer exposes the Engine over HTTP. All endpoints are read-only
// except /build (POST) and /reset (POST). No external dependencies.
//
// Endpoints:
//
//	GET  /health         — liveness probe (200 OK)
//	GET  /metrics        — Prometheus text metrics
//	GET  /status         — JSON snapshot of DAG state + queue stats
//	GET  /plan?target=X  — JSON dry-run BuildPlan
//	GET  /analysis       — JSON graph structural analysis
//	GET  /dot?title=X    — Graphviz DOT export
//	GET  /progress       — SSE stream of EventBus events
//	POST /build          — trigger a build (JSON body: BuildRequest)
//	POST /reset          — reset all vertices to StateInitial
type BuildServer struct {
	engine *Engine
	queue  *BuildQueue
	mux    *http.ServeMux
}

// NewBuildServer constructs a BuildServer.
// queue may be nil; in that case /build runs synchronously.
func NewBuildServer(engine *Engine, queue *BuildQueue) *BuildServer {
	s := &BuildServer{engine: engine, queue: queue}
	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

// Handler returns the http.Handler to mount on a server or test client.
func (s *BuildServer) Handler() http.Handler { return s.mux }

// ListenAndServe starts the HTTP server on addr (e.g. ":8080").
func (s *BuildServer) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.mux)
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

func (s *BuildServer) registerRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/plan", s.handlePlan)
	s.mux.HandleFunc("/analysis", s.handleAnalysis)
	s.mux.HandleFunc("/dot", s.handleDOT)
	s.mux.HandleFunc("/progress", s.handleProgress)
	s.mux.HandleFunc("/build", s.handleBuild)
	s.mux.HandleFunc("/reset", s.handleReset)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// GET /health — liveness probe.
func (s *BuildServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /metrics — Prometheus text format.
func (s *BuildServer) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.engine.WriteMetrics(w)
}

// GET /status — JSON DAG state snapshot + queue stats.
func (s *BuildServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.engine.Snapshot()

	type response struct {
		Snapshot  DAGSnapshot  `json:"snapshot"`
		Cache     ManagedStoreStats `json:"cache"`
		Queue     *QueueStats  `json:"queue,omitempty"`
		Stalls    []StallReport `json:"stalls,omitempty"`
		CapturedAt time.Time   `json:"captured_at"`
	}

	resp := response{
		Snapshot:   snap,
		Cache:      s.engine.CacheStats(),
		Stalls:     s.engine.DetectStalls(30 * time.Second),
		CapturedAt: time.Now(),
	}
	if s.queue != nil {
		qs := s.queue.Stats()
		resp.Queue = &qs
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /plan?target=X&changed=/path1,/path2 — dry-run plan.
func (s *BuildServer) handlePlan(w http.ResponseWriter, r *http.Request) {
	targetID := r.URL.Query().Get("target")
	if targetID == "" {
		writeError(w, http.StatusBadRequest, "missing query param: target")
		return
	}

	var changedFiles []FileRef
	if paths := r.URL.Query().Get("changed"); paths != "" {
		for _, p := range strings.Split(paths, ",") {
			changedFiles = append(changedFiles, FileRef{Path: strings.TrimSpace(p)})
		}
	}

	plan, err := s.engine.Plan(r.Context(), targetID, changedFiles)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// GET /analysis — graph structural analysis.
func (s *BuildServer) handleAnalysis(w http.ResponseWriter, _ *http.Request) {
	a, err := s.engine.Analyse()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// GET /dot?title=X — Graphviz DOT export.
func (s *BuildServer) handleDOT(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	if title == "" {
		title = "reactdag"
	}
	w.Header().Set("Content-Type", "text/vnd.graphviz; charset=utf-8")
	fmt.Fprint(w, s.engine.ExportDOT(title))
}

// GET /progress — Server-Sent Events stream of build events.
// Each SSE event carries a JSON-encoded Event payload.
func (s *BuildServer) handleProgress(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	obs := s.engine.Observe(WithBufferSize(128))
	defer func() {
		obs.Unsubscribe()
		obs.Drain()
	}()

	for {
		select {
		case e, ok := <-obs.Events():
			if !ok {
				return
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

// POST /build — trigger a build.
// Body (JSON): { "target_id": "X", "priority": 0, "changed_files": [...] }
func (s *BuildServer) handleBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		TargetID     string            `json:"target_id"`
		Priority     int               `json:"priority"`
		ChangedFiles []FileRef         `json:"changed_files"`
		Labels       map[string]string `json:"labels"`
		DeadlineSec  int               `json:"deadline_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if req.TargetID == "" {
		writeError(w, http.StatusBadRequest, "target_id required")
		return
	}

	buildReq := BuildRequest{
		ID:           fmt.Sprintf("http-%d", time.Now().UnixNano()),
		TargetID:     req.TargetID,
		ChangedFiles: req.ChangedFiles,
		Priority:     req.Priority,
		Labels:       req.Labels,
	}
	if req.DeadlineSec > 0 {
		buildReq.Deadline = time.Now().Add(time.Duration(req.DeadlineSec) * time.Second)
	}

	if s.queue != nil {
		// Async: submit to queue and return 202 Accepted with the request ID.
		s.queue.Submit(buildReq)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"request_id": buildReq.ID,
			"status":     "queued",
		})
		return
	}

	// Synchronous fallback: run build inline and return 200 with result.
	result := s.engine.Build(r.Context(), buildReq.TargetID, buildReq.ChangedFiles)
	statusCode := http.StatusOK
	if result.Error != nil {
		statusCode = http.StatusInternalServerError
	}
	writeJSON(w, statusCode, map[string]interface{}{
		"request_id": buildReq.ID,
		"executed":   result.Metrics.Executed,
		"cached":     result.Metrics.FastCacheHits + result.Metrics.SlowCacheHits,
		"failed":     result.Metrics.Failed,
		"duration_ms": result.Duration.Milliseconds(),
		"error":      errString(result.Error),
	})
}

// POST /reset — reset all vertices to StateInitial.
func (s *BuildServer) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	s.engine.ResetAll()
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// Middleware for BuildServer
// ---------------------------------------------------------------------------

// WithCORS wraps a handler to allow cross-origin requests.
// origins is a comma-separated list of allowed origins ("*" = all).
func WithCORS(h http.Handler, origins string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origins)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// WithRequestLog wraps a handler to log every HTTP request to logFn.
func WithRequestLog(h http.Handler, logFn func(method, path string, status int, dur time.Duration)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rw, r)
		logFn(r.Method, r.URL.Path, rw.status, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
