package handler

import (
	"net/http"

	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

// HealthHandler handles /healthz and /readyz.
type HealthHandler struct {
	store  store.Store
	logger zerolog.Logger
}

// NewHealthHandler constructs a HealthHandler.
func NewHealthHandler(s store.Store, logger zerolog.Logger) *HealthHandler {
	return &HealthHandler{store: s, logger: logger}
}

// Liveness handles GET /healthz — always returns 200 if the process is alive.
func (h *HealthHandler) Liveness(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Readiness handles GET /readyz — returns 200 only if the DB is reachable.
func (h *HealthHandler) Readiness(c *gin.Context) {
	if err := h.store.Ping(c.Request.Context()); err != nil {
		h.logger.Error().Err(err).Msg("readiness probe: DB ping failed")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unavailable",
			"reason": "database unreachable",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
