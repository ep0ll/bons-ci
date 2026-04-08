// Package health provides HTTP handlers for Kubernetes liveness and readiness
// probes, and a simple /healthz endpoint for load balancer checks.
//
// Design: health checks are decoupled from business logic via the Checker
// interface. Components register their own health checks; the handler
// aggregates them without knowing their internals.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Status represents the health status of a component.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

// CheckResult is returned by each registered Checker.
type CheckResult struct {
	Name    string            `json:"name"`
	Status  Status            `json:"status"`
	Message string            `json:"message,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

// Checker is the interface each component must implement to report its health.
type Checker interface {
	Check(ctx context.Context) CheckResult
}

// CheckerFunc adapts a function to the Checker interface.
type CheckerFunc func(ctx context.Context) CheckResult

func (f CheckerFunc) Check(ctx context.Context) CheckResult { return f(ctx) }

// Response is the JSON envelope returned by health endpoints.
type Response struct {
	Status    Status        `json:"status"`
	Timestamp time.Time     `json:"timestamp"`
	Checks    []CheckResult `json:"checks"`
}

// Handler aggregates registered Checkers and serves /healthz, /livez, /readyz.
type Handler struct {
	mu       sync.RWMutex
	checkers []Checker
	log      *slog.Logger
}

// NewHandler returns a Handler with no registered checkers.
func NewHandler(log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{log: log}
}

// Register adds a Checker to the aggregation set.
// Safe to call concurrently and after the server has started.
func (h *Handler) Register(c Checker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkers = append(h.checkers, c)
}

// LivezHandler responds 200 OK as long as the process is running.
// Kubernetes uses this to decide whether to restart the pod.
func (h *Handler) LivezHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `{"status":"alive"}`)
}

// ReadyzHandler runs all registered checkers and responds 200 only if all
// report healthy. 503 if any checker is degraded or unhealthy.
// Kubernetes uses this to decide whether to route traffic to the pod.
func (h *Handler) ReadyzHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	h.mu.RLock()
	checkers := make([]Checker, len(h.checkers))
	copy(checkers, h.checkers)
	h.mu.RUnlock()

	results := make([]CheckResult, 0, len(checkers))
	overall := StatusHealthy

	for _, c := range checkers {
		res := c.Check(ctx)
		results = append(results, res)
		if res.Status == StatusUnhealthy {
			overall = StatusUnhealthy
		} else if res.Status == StatusDegraded && overall != StatusUnhealthy {
			overall = StatusDegraded
		}
	}

	resp := Response{
		Status:    overall,
		Timestamp: time.Now().UTC(),
		Checks:    results,
	}

	statusCode := http.StatusOK
	if overall != StatusHealthy {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Error("failed to encode health response", "error", err)
	}
}

// RegisterRoutes mounts all health endpoints on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.LivezHandler)
	mux.HandleFunc("/livez", h.LivezHandler)
	mux.HandleFunc("/readyz", h.ReadyzHandler)
}

// --- EventBusChecker ────────────────────────────────────────────────────────

// EventBusClosedChecker reports healthy as long as the bus is not closed.
// Extend this with actual queue depth metrics for production alerting.
type EventBusClosedChecker struct {
	isClosed func() bool
}

func NewEventBusClosedChecker(isClosed func() bool) Checker {
	return &EventBusClosedChecker{isClosed: isClosed}
}

func (c *EventBusClosedChecker) Check(_ context.Context) CheckResult {
	if c.isClosed() {
		return CheckResult{Name: "event_bus", Status: StatusUnhealthy, Message: "bus is closed"}
	}
	return CheckResult{Name: "event_bus", Status: StatusHealthy}
}
