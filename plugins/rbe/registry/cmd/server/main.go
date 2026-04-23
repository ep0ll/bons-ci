// Package main is the AccelRegistry server entrypoint.
//
// Configuration via environment variables:
//
//	ACCELREG_ADDR             HTTP listen address (default: ":5000")
//	ACCELREG_METRICS_ADDR     Prometheus metrics listen address (default: ":9090")
//	ACCELREG_LOG_LEVEL        Log level: debug, info, warn, error (default: "info")
//	ACCELREG_EXPECTED_SOURCES Approximate unique source images (default: "100000")
//	ACCELREG_READ_TIMEOUT     HTTP read timeout (default: "30s")
//	ACCELREG_WRITE_TIMEOUT    HTTP write timeout (default: "60s")
//	ACCELREG_IDLE_TIMEOUT     HTTP idle timeout (default: "120s")
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	apiv1 "github.com/bons/bons-ci/plugins/rbe/registry/api/v1"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/logger"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/middleware"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/registry"
)

func main() {
	log := buildLogger(getenv("ACCELREG_LOG_LEVEL", "info"))
	defer func() { _ = log.Sync() }()

	expectedSources, _ := strconv.ParseUint(getenv("ACCELREG_EXPECTED_SOURCES", "100000"), 10, 64)

	reg, err := registry.New(registry.Config{
		ExpectedSources: expectedSources,
		Log:             log,
	})
	if err != nil {
		log.Fatal("failed to create registry", logger.Error(err))
	}

	apiHandler := apiv1.New(reg, log)

	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(middleware.Recovery(log))
	r.Use(middleware.Logger(log))
	r.Use(middleware.PrometheusMetrics())
	r.Use(middleware.CORS())
	r.Use(middleware.RequireJSON())
	r.Use(chimiddleware.Compress(5))
	r.Mount("/", apiHandler.Router())

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	addr := getenv("ACCELREG_ADDR", ":5000")
	readTimeout := parseDuration(getenv("ACCELREG_READ_TIMEOUT", "30s"), 30*time.Second)
	writeTimeout := parseDuration(getenv("ACCELREG_WRITE_TIMEOUT", "60s"), 60*time.Second)
	idleTimeout := parseDuration(getenv("ACCELREG_IDLE_TIMEOUT", "120s"), 120*time.Second)

	server := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	metricsAddr := getenv("ACCELREG_METRICS_ADDR", ":9090")
	metricsServer := &http.Server{
		Addr:         metricsAddr,
		Handler:      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("metrics")) }),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("accelregistry listening",
			logger.String("addr", addr),
			logger.String("metricsAddr", metricsAddr),
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("server error", logger.Error(err))
		}
	}()

	go func() {
		log.Info("metrics server listening", logger.String("addr", metricsAddr))
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("metrics server error", logger.Error(err))
		}
	}()

	<-ctx.Done()
	log.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	_ = metricsServer.Shutdown(shutdownCtx)
	log.Info("server stopped gracefully")
}

func buildLogger(level string) *logger.Logger {
	_ = level
	return logger.NewProduction()
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
