// Package api wires all HTTP handlers and middleware into a Gin engine.
package api

import (
	"github.com/bons/bons-ci/plugins/byoc/internal/api/handler"
	"github.com/bons/bons-ci/plugins/byoc/internal/api/middleware"
	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

// RouterConfig groups all handler dependencies for the router factory.
type RouterConfig struct {
	TenantHandler  *handler.TenantHandler
	WebhookHandler *handler.WebhookHandler
	HealthHandler  *handler.HealthHandler
	Metrics        *observability.Metrics
	Logger         zerolog.Logger
	Debug          bool
}

// NewRouter builds and returns a configured Gin engine.
// All middleware is applied in order: RequestID → Logger → Recovery → Metrics.
func NewRouter(cfg RouterConfig) *gin.Engine {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(
		middleware.RequestID(),
		middleware.StructuredLogger(cfg.Logger),
		middleware.Recovery(cfg.Logger),
		middleware.PrometheusMetrics(cfg.Metrics),
	)

	// Health & observability — no auth required.
	r.GET("/healthz", cfg.HealthHandler.Liveness)
	r.GET("/readyz", cfg.HealthHandler.Readiness)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Webhook endpoint — authenticated via HMAC signature validation inside the handler.
	webhooks := r.Group("/webhooks")
	{
		webhooks.POST("/github/:tenant_id", cfg.WebhookHandler.Handle)
	}

	// Tenant management API.
	v1 := r.Group("/v1")
	{
		tenants := v1.Group("/tenants")
		{
			tenants.POST("", cfg.TenantHandler.Create)
			tenants.GET("/:id", cfg.TenantHandler.Get)
			tenants.DELETE("/:id", cfg.TenantHandler.Delete)
			tenants.GET("/:id/runners", cfg.TenantHandler.ListRunners)
		}
	}

	return r
}
