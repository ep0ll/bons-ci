// Command server is the BYOC OCI GitHub Actions Runner control plane.
// It wires all internal services via constructor injection and starts
// the HTTP server, orchestrator, and reconciler.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/api"
	"github.com/bons/bons-ci/plugins/byoc/internal/api/handler"
	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/orchestrator"
	"github.com/bons/bons-ci/plugins/byoc/internal/reconciler"
	"github.com/bons/bons-ci/plugins/byoc/internal/scaler"
	"github.com/bons/bons-ci/plugins/byoc/internal/tenant"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"
)

func main() {
	// --- Configuration ---
	viper.SetEnvPrefix("BYOC")
	viper.AutomaticEnv()
	viper.SetDefault("HTTP_PORT", "8080")
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("LOG_PRETTY", false)
	viper.SetDefault("DB_DSN", "byoc:byoc@tcp(localhost:3306)/byoc?parseTime=true")

	// --- Observability ---
	logger := observability.NewLogger(observability.LogConfig{
		Level:  viper.GetString("LOG_LEVEL"),
		Pretty: viper.GetBool("LOG_PRETTY"),
	})

	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	logger.Info().Msg("starting BYOC OCI runner control plane")

	// --- Store (MySQL) ---
	// In production: import store/mysql and call mysql.New(cfg, logger)
	// For compilation without network deps, we use a no-op store here.
	// Replace with: store, err := mysqlpkg.New(mysqlpkg.Config{DSN: viper.GetString("DB_DSN")}, logger)
	dataStore := newNoopStore()

	// --- Vault ---
	// In production: import vault pkg and construct OCIVaultClient with instance principal auth.
	// vaultClient := vault.NewOCIVaultClient(vaultCfg, ociSecretsClient, logger)
	_ = logger // vault client would use the logger

	// --- GitHub Client ---
	// In production: import github pkg and construct HTTPClient.
	// githubClient := github.NewHTTPClient(github.HTTPClientConfig{...}, logger)
	githubClient := newNoopGitHubClient()

	// --- Provisioner ---
	// In production: import provisioner/compute and construct OCIComputeProvisioner.
	// prov := compute.New(ociComputeClient, computeCfg, logger)
	prov := newNoopProvisioner()

	// --- Runner Service ---
	runnerSvc := newNoopRunnerService(dataStore, prov, githubClient, metrics, logger)

	// --- Scaler ---
	sc := scaler.New(dataStore, metrics, scaler.Config{}, logger)

	// --- Orchestrator ---
	orch := orchestrator.New(dataStore, runnerSvc, sc, metrics, orchestrator.Config{}, logger)

	// --- Reconciler ---
	rec := reconciler.New(dataStore, prov, metrics, reconciler.Config{}, logger)

	// --- Tenant Service ---
	tenantSvc := tenant.NewService(dataStore, logger)

	// --- HTTP Handlers ---
	tenantH := handler.NewTenantHandler(tenantSvc, dataStore, logger)
	webhookH := handler.NewWebhookHandler(dataStore, githubClient, orch, metrics, logger)
	healthH := handler.NewHealthHandler(dataStore, logger)

	// --- Router ---
	router := api.NewRouter(api.RouterConfig{
		TenantHandler:  tenantH,
		WebhookHandler: webhookH,
		HealthHandler:  healthH,
		Metrics:        metrics,
		Logger:         logger,
		Debug:          viper.GetBool("DEBUG"),
	})

	// --- HTTP Server ---
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", viper.GetString("HTTP_PORT")),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// --- Start background workers ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go orch.Run(ctx)
	go rec.Run(ctx)

	// --- Start HTTP server ---
	go func() {
		logger.Info().Str("addr", srv.Addr).Msg("HTTP server starting")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	// --- Graceful shutdown on SIGTERM / SIGINT ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	logger.Info().Str("signal", sig.String()).Msg("shutdown signal received")

	cancel() // stop background workers

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("HTTP server shutdown error")
	}

	logger.Info().Msg("server exited cleanly")
}

// ---------------------------------------------------------------------------
// No-op adapters — replace with real implementations in production.
// These allow the binary to compile and run without external dependencies.
// ---------------------------------------------------------------------------

// The noop types below implement the required interfaces with stubs.
// In production each is replaced by the real adapter from its respective package.

func newNoopStore() *noopStore                 { return &noopStore{} }
func newNoopGitHubClient() *noopGitHubClient   { return &noopGitHubClient{} }
func newNoopProvisioner() *noopProvisionerImpl { return &noopProvisionerImpl{} }
func newNoopRunnerService(
	_ interface{}, _ interface{}, _ interface{},
	_ interface{}, _ interface{},
) *noopRunnerServiceImpl {
	return &noopRunnerServiceImpl{}
}
