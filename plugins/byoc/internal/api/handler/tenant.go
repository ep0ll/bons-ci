// Package handler contains the HTTP handlers for the BYOC API.
// Handlers are thin: they decode/validate input, delegate to a domain service,
// and encode the response. No business logic lives here.
package handler

import (
	"net/http"

	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/bons/bons-ci/plugins/byoc/internal/tenant"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

// TenantHandler handles /v1/tenants endpoints.
type TenantHandler struct {
	svc    *tenant.Service
	store  store.Store
	logger zerolog.Logger
}

// NewTenantHandler constructs a TenantHandler.
func NewTenantHandler(svc *tenant.Service, s store.Store, logger zerolog.Logger) *TenantHandler {
	return &TenantHandler{svc: svc, store: s, logger: logger}
}

// Create handles POST /v1/tenants.
func (h *TenantHandler) Create(c *gin.Context) {
	var req tenant.CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}

	t, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		if store.IsConflict(err) {
			respondError(c, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		h.logger.Error().Err(err).Msg("create tenant")
		respondError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create tenant")
		return
	}

	respondOK(c, http.StatusCreated, t)
}

// Get handles GET /v1/tenants/:id.
func (h *TenantHandler) Get(c *gin.Context) {
	id := c.Param("id")
	t, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		if store.IsNotFound(err) {
			respondError(c, http.StatusNotFound, "NOT_FOUND", "tenant not found")
			return
		}
		respondError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get tenant")
		return
	}
	// Scrub webhook secret from response.
	t.WebhookSecret = ""
	respondOK(c, http.StatusOK, t)
}

// ListRunners handles GET /v1/tenants/:id/runners.
func (h *TenantHandler) ListRunners(c *gin.Context) {
	tenantID := c.Param("id")
	runners, err := h.store.ListRunners(c.Request.Context(), store.RunnerFilter{
		TenantID: &tenantID,
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list runners")
		return
	}
	respondOK(c, http.StatusOK, runners)
}

// Delete handles DELETE /v1/tenants/:id (offboard).
func (h *TenantHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.Offboard(c.Request.Context(), id); err != nil {
		if store.IsNotFound(err) {
			respondError(c, http.StatusNotFound, "NOT_FOUND", "tenant not found")
			return
		}
		respondError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to offboard tenant")
		return
	}
	respondOK(c, http.StatusOK, gin.H{"status": "offboarding"})
}

// --- shared response helpers ---

type apiResponse struct {
	Data      interface{} `json:"data"`
	Error     interface{} `json:"error"`
	RequestID string      `json:"request_id"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func respondOK(c *gin.Context, status int, data interface{}) {
	c.JSON(status, apiResponse{
		Data:      data,
		Error:     nil,
		RequestID: c.GetString("request_id"),
	})
}

func respondError(c *gin.Context, status int, code, msg string) {
	c.JSON(status, apiResponse{
		Data: nil,
		Error: apiError{
			Code:    code,
			Message: msg,
		},
		RequestID: c.GetString("request_id"),
	})
}
