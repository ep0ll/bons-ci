// Package middleware provides Gin middleware for the BYOC HTTP API.
package middleware

import (
	"net/http"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// RequestID attaches a unique request ID to every request context and response header.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		c.Set("request_id", reqID)
		c.Header("X-Request-ID", reqID)

		// Propagate into context for structured logging.
		ctx := observability.WithRequestID(c.Request.Context(), reqID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// StructuredLogger logs every request with zerolog, including latency, status, and IDs.
func StructuredLogger(logger zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)

		log := logger.Info()
		if c.Writer.Status() >= 500 {
			log = logger.Error()
		} else if c.Writer.Status() >= 400 {
			log = logger.Warn()
		}

		log.
			Str("method", c.Request.Method).
			Str("path", c.FullPath()).
			Int("status", c.Writer.Status()).
			Dur("latency", latency).
			Str("client_ip", c.ClientIP()).
			Str("request_id", c.GetString("request_id")).
			Msg("http request")
	}
}

// Recovery catches panics and returns 500 instead of crashing the process.
func Recovery(logger zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				logger.Error().
					Interface("panic", err).
					Str("path", c.Request.URL.Path).
					Msg("recovered from panic")
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"data":  nil,
					"error": gin.H{"code": "INTERNAL_ERROR", "message": "internal server error"},
				})
			}
		}()
		c.Next()
	}
}

// PrometheusMetrics records HTTP request duration and status metrics.
func PrometheusMetrics(metrics *observability.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		metrics.APIRequestDuration.WithLabelValues(
			c.Request.Method,
			c.FullPath(),
			http.StatusText(c.Writer.Status()),
		).Observe(time.Since(start).Seconds())
	}
}
