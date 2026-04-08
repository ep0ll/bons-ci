// Package bootstrap wires all dependencies together from a Config.
// It is the only place that knows about concrete types — everything else
// depends on interfaces.
//
// Dependency order (leaf → root):
//  1. Observability (logger, metrics, tracer) — no deps
//  2. KeyProvider — depends on config
//  3. Signer — depends on KeyProvider + config
//  4. ResiliencePolicy — depends on metrics
//  5. IdempotencyStore — no deps
//  6. EventBus — depends on metrics + logger
//  7. SigningService — depends on all of the above
//  8. HTTP server — depends on health handler + metrics
//  9. App — holds everything, drives lifecycle
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bons/bons-ci/pkg/sigstore/internal/config"
	"github.com/bons/bons-ci/pkg/sigstore/internal/eventbus"
	"github.com/bons/bons-ci/pkg/sigstore/internal/health"
	"github.com/bons/bons-ci/pkg/sigstore/internal/idempotency"
	"github.com/bons/bons-ci/pkg/sigstore/internal/keyprovider"
	"github.com/bons/bons-ci/pkg/sigstore/internal/observability"
	"github.com/bons/bons-ci/pkg/sigstore/internal/resilience"
	"github.com/bons/bons-ci/pkg/sigstore/internal/service"
	"github.com/bons/bons-ci/pkg/sigstore/internal/signing"
)

// App holds the fully wired application. Callers interact with it via
// Start and Shutdown — no field access needed.
type App struct {
	signingService *service.SigningService
	httpServer     *http.Server
	metricsServer  *http.Server
	bus            eventbus.EventBus
	tracer         *observability.TracerProvider
	log            *slog.Logger
}

// Wire constructs the full dependency graph from cfg.
// Returns a ready-to-run App or an error if any dependency fails validation.
func Wire(ctx context.Context, cfg *config.Config) (*App, error) {
	// ── 1. Observability ────────────────────────────────────────────────────
	log := observability.NewLogger(observability.LogConfig{
		Level:  cfg.Observability.SlogLevel(),
		Format: cfg.Observability.LogFormat,
	})

	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	tracer, err := observability.NewTracerProvider(ctx, observability.TracingConfig{
		ServiceName:    cfg.Observability.ServiceName,
		ServiceVersion: cfg.Observability.ServiceVersion,
		OTLPEndpoint:   cfg.Observability.OTLPEndpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("wire tracer: %w", err)
	}

	// ── 2. Key provider ─────────────────────────────────────────────────────
	kp, err := buildKeyProvider(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("wire key provider: %w", err)
	}

	// ── 3. Signer ───────────────────────────────────────────────────────────
	signer, err := buildSigner(cfg, kp, log, metrics)
	if err != nil {
		return nil, fmt.Errorf("wire signer: %w", err)
	}

	// ── 4. Resilience policy ─────────────────────────────────────────────────
	cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
		FailureThreshold: cfg.Resilience.CircuitBreaker.FailureThreshold,
		SuccessThreshold: cfg.Resilience.CircuitBreaker.SuccessThreshold,
		OpenDuration:     cfg.Resilience.CircuitBreaker.OpenDuration,
		Name:             "signing",
		Logger:           log,
		Metrics:          metrics,
	})
	retry := resilience.NewRetryPolicy(resilience.RetryConfig{
		MaxAttempts: cfg.Resilience.Retry.MaxAttempts,
		InitialWait: cfg.Resilience.Retry.InitialWait,
		MaxWait:     cfg.Resilience.Retry.MaxWait,
		Logger:      log,
		Metrics:     metrics,
	})
	policy := resilience.NewComposedPolicy(cb, retry)

	// ── 5. Idempotency store ─────────────────────────────────────────────────
	idemStore := idempotency.NewMemoryIdempotencyStore()

	// ── 6. Event bus ─────────────────────────────────────────────────────────
	bus := eventbus.NewMemoryBus(eventbus.MemoryBusConfig{
		BufferSize:        cfg.EventBus.BufferSize,
		WorkersPerTopic:   cfg.EventBus.WorkersPerTopic,
		HandlerTimeout:    cfg.EventBus.HandlerTimeout,
		MaxHandlerRetries: cfg.EventBus.MaxHandlerRetries,
		Logger:            log,
		Metrics:           metrics,
	})

	// ── 7. Signing service ───────────────────────────────────────────────────
	svc, err := service.NewSigningService(service.Config{
		Bus:              bus,
		Signer:           signer,
		ResiliencePolicy: policy,
		IdempotencyStore: idemStore,
		Tracer:           tracer.Tracer("signing_service"),
		Logger:           log,
		Metrics:          metrics,
		IdempotencyTTL:   10 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("wire signing service: %w", err)
	}
	if err = svc.RegisterHandlers(); err != nil {
		return nil, fmt.Errorf("register handlers: %w", err)
	}

	// ── 8. HTTP servers ──────────────────────────────────────────────────────
	healthHandler := health.NewHandler(log)
	mainMux := http.NewServeMux()
	healthHandler.RegisterRoutes(mainMux)
	// Mount signing API handler here when added (extend without modifying service)
	httpServer := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      mainMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	var metricsServer *http.Server
	if cfg.Observability.MetricsAddr != "" {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		metricsServer = &http.Server{
			Addr:        cfg.Observability.MetricsAddr,
			Handler:     metricsMux,
			ReadTimeout: 5 * time.Second,
		}
	}

	log.Info("dependency graph wired",
		"signing_backend", cfg.Signing.Backend,
		"server_addr", cfg.Server.Addr,
		"metrics_addr", cfg.Observability.MetricsAddr,
	)

	return &App{
		signingService: svc,
		httpServer:     httpServer,
		metricsServer:  metricsServer,
		bus:            bus,
		tracer:         tracer,
		log:            log,
	}, nil
}

// Start begins serving traffic. It is blocking; callers should run it
// in a goroutine and signal shutdown via context cancellation.
func (a *App) Start(ctx context.Context) error {
	errCh := make(chan error, 2)

	go func() {
		a.log.Info("HTTP server starting", "addr", a.httpServer.Addr)
		if err := a.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	if a.metricsServer != nil {
		go func() {
			a.log.Info("metrics server starting", "addr", a.metricsServer.Addr)
			if err := a.metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("metrics server: %w", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

// Shutdown performs a graceful two-phase teardown:
//  1. Stop accepting new HTTP requests (HTTP server shutdown)
//  2. Drain the event bus (in-flight events complete)
//  3. Flush OTel spans
func (a *App) Shutdown(ctx context.Context) error {
	a.log.Info("shutdown initiated")
	var errs []error

	if err := a.httpServer.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("http server shutdown: %w", err))
	}
	if a.metricsServer != nil {
		if err := a.metricsServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("metrics server shutdown: %w", err))
		}
	}
	if err := a.bus.Close(); err != nil {
		errs = append(errs, fmt.Errorf("event bus close: %w", err))
	}
	if err := a.tracer.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("tracer shutdown: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	a.log.Info("shutdown complete")
	return nil
}

// --- builder helpers ─────────────────────────────────────────────────────────

func buildKeyProvider(cfg *config.Config, log *slog.Logger) (keyprovider.KeyProvider, error) {
	switch cfg.Signing.Backend {
	case "keyless":
		// Keyless flow does not use a KeyProvider (ephemeral key generated inline)
		return nil, nil //nolint:nilnil // intentional: keyless has no KeyProvider
	case "static_key":
		key, err := keyprovider.LoadECDSAKeyFromFile(cfg.Signing.StaticKey.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("load static key: %w", err)
		}
		return keyprovider.NewStaticKeyProvider([]keyprovider.KeyEntry{
			{Name: cfg.Signing.StaticKey.KeyName, Signer: key},
		})
	case "kms":
		return keyprovider.NewKMSKeyProvider(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q", cfg.Signing.Backend)
	}
}

func buildSigner(
	cfg *config.Config,
	kp keyprovider.KeyProvider,
	log *slog.Logger,
	metrics *observability.Metrics,
) (signing.Signer, error) {
	switch cfg.Signing.Backend {
	case "keyless":
		return signing.NewKeylessSigner(signing.KeylessSignerConfig{
			FulcioURL:    cfg.Signing.Keyless.FulcioURL,
			RekorURL:     cfg.Signing.Keyless.RekorURL,
			OIDCIssuer:   cfg.Signing.Keyless.OIDCIssuer,
			OIDCClientID: cfg.Signing.Keyless.OIDCClientID,
			Logger:       log,
			Metrics:      metrics,
		})
	case "static_key":
		return signing.NewStaticKeySigner(signing.StaticKeySignerConfig{
			KeyProvider:            kp,
			RekorURL:               cfg.Signing.StaticKey.RekorURL,
			AttachToRekorByDefault: true,
			Logger:                 log,
			Metrics:                metrics,
		})
	case "kms":
		return signing.NewStaticKeySigner(signing.StaticKeySignerConfig{
			KeyProvider:            kp,
			RekorURL:               cfg.Signing.KMS.RekorURL,
			AttachToRekorByDefault: true,
			Logger:                 log,
			Metrics:                metrics,
		})
	default:
		return nil, fmt.Errorf("unknown signing backend %q", cfg.Signing.Backend)
	}
}
