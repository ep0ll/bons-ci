// Command rbed is the Remote Build Environment daemon. It provides:
//
//   - An OCI Distribution Spec v1 compliant image registry with extensions for
//     Nydus, eStargz, zstd:chunked, and OverlayBD accelerated image formats.
//   - A DAG-based build graph API for tracking LLB / build vertices.
//   - Vertex-level result caching with content-addressed keys.
//   - Full FD log streaming (stdin/stdout/stderr + any fd) per build vertex.
//   - BuildKit-style persistent mount caches with platform isolation.
//   - Attestation storage: SBOM (SPDX/CycloneDX), SLSA provenance, cosign/sigstore.
//   - Image conversion tracking (OCI→Nydus, OCI→eStargz, etc.).
//   - Dual HTTP + gRPC API with JWT/OIDC + mTLS + API-key layered auth.
//   - OpenTelemetry tracing and Prometheus metrics.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/bons/bons-ci/plugins/rbe/pkg/api/grpc"
	rbehttpapi "github.com/bons/bons-ci/plugins/rbe/pkg/api/http"
	"github.com/bons/bons-ci/plugins/rbe/pkg/attestation"
	"github.com/bons/bons-ci/plugins/rbe/pkg/auth"
	"github.com/bons/bons-ci/plugins/rbe/pkg/config"
	"github.com/bons/bons-ci/plugins/rbe/pkg/dag"
	localstore "github.com/bons/bons-ci/plugins/rbe/pkg/storage/local"
	s3store "github.com/bons/bons-ci/plugins/rbe/pkg/storage/s3"
	etcdmeta "github.com/bons/bons-ci/plugins/rbe/pkg/metadata/etcd"
	tikvmeta "github.com/bons/bons-ci/plugins/rbe/pkg/metadata/tikv"
	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
	"github.com/bons/bons-ci/plugins/rbe/pkg/mountcache"
	"github.com/bons/bons-ci/plugins/rbe/pkg/observability"
	"github.com/bons/bons-ci/plugins/rbe/pkg/registry"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
)

func main() {
	cfgFile := flag.String("config", "", "Path to YAML config file (default: auto-discover)")
	flag.Parse()

	cfg, err := config.Load(*cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v", err)
		os.Exit(1)
	}

	// ── Logging & tracing ────────────────────────────────────────────────
	observability.SetupLogging(cfg.Observability.LogLevel)
	log := observability.Logger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracing, err := observability.SetupTracing(ctx,
		cfg.Observability.OTLPEndpoint,
		cfg.Observability.ServiceName,
		cfg.Observability.ServiceVersion,
	)
	if err != nil {
		log.Warn().Err(err).Msg("tracing setup failed; continuing without traces")
	}
	defer func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(ctx2)
	}()

	// ── Storage backend ──────────────────────────────────────────────────
	var blobStore storage.Store
	switch cfg.Storage.Backend {
	case "s3":
		log.Info().Str("endpoint", cfg.Storage.S3.Endpoint).Str("bucket", cfg.Storage.S3.Bucket).Msg("using S3 blob store")
		blobStore, err = s3store.New(ctx, s3store.Config{
			Endpoint:          cfg.Storage.S3.Endpoint,
			Region:            cfg.Storage.S3.Region,
			Bucket:            cfg.Storage.S3.Bucket,
			AccessKeyID:       cfg.Storage.S3.AccessKeyID,
			SecretAccessKey:   cfg.Storage.S3.SecretAccessKey,
			ForcePathStyle:    cfg.Storage.S3.ForcePathStyle,
			KeyPrefix:         cfg.Storage.S3.KeyPrefix,
			UploadConcurrency: cfg.Storage.S3.UploadConcurrency,
		})
	case "local", "":
		log.Info().Str("root", cfg.Storage.Local.Root).Msg("using local blob store")
		blobStore, err = localstore.New(ctx, localstore.Config{Root: cfg.Storage.Local.Root})
	default:
		err = fmt.Errorf("unknown storage backend: %s", cfg.Storage.Backend)
	}
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise blob store")
	}
	defer blobStore.Close()

	// ── Metadata backend ─────────────────────────────────────────────────
	var metaStore metadata.Store
	switch cfg.Metadata.Backend {
	case "etcd", "":
		log.Info().Strs("endpoints", cfg.Metadata.Etcd.Endpoints).Msg("using etcd metadata store")
		metaStore, err = etcdmeta.New(etcdmeta.Config{
			Endpoints:   cfg.Metadata.Etcd.Endpoints,
			DialTimeout: cfg.Metadata.Etcd.DialTimeout,
			Username:    cfg.Metadata.Etcd.Username,
			Password:    cfg.Metadata.Etcd.Password,
			CertFile:    cfg.Metadata.Etcd.CertFile,
			KeyFile:     cfg.Metadata.Etcd.KeyFile,
			CAFile:      cfg.Metadata.Etcd.CAFile,
		})
	case "tikv":
		log.Info().Strs("pd", cfg.Metadata.TiKV.PDAddresses).Msg("using TiKV metadata store")
		metaStore, err = tikvmeta.New(tikvmeta.Config{PDAddresses: cfg.Metadata.TiKV.PDAddresses})
	default:
		err = fmt.Errorf("unknown metadata backend: %s", cfg.Metadata.Backend)
	}
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise metadata store")
	}
	defer metaStore.Close()

	// ── Domain services ──────────────────────────────────────────────────
	registryService := registry.New(blobStore, metaStore)
	dagService      := dag.New(metaStore)
	cacheService    := dag.NewCacheService(metaStore)
	logService      := dag.NewLogService(metaStore, blobStore)
	mountCacheSvc   := mountcache.New(metaStore, blobStore)
	attestationSvc  := attestation.New(metaStore, blobStore)

	svcHTTP := rbehttpapi.Services{
		Registry:    registryService,
		DAG:         dagService,
		Cache:       cacheService,
		Logs:        logService,
		MountCache:  mountCacheSvc,
		Attestation: attestationSvc,
	}
	svcGRPC := grpc.Services{
		Registry:    registryService,
		DAG:         dagService,
		Cache:       cacheService,
		Logs:        logService,
		MountCache:  mountCacheSvc,
		Attestation: attestationSvc,
	}

	// ── Auth ─────────────────────────────────────────────────────────────
	authMw, err := auth.NewMiddleware(auth.Config{
		JWTSigningKey: []byte(cfg.Auth.JWTSigningKey),
		JWTPublicKeys: cfg.Auth.JWTPublicKeys,
		OIDCIssuer:    cfg.Auth.OIDCIssuer,
		OIDCAudience:  cfg.Auth.OIDCAudience,
		JWKSEndpoint:  cfg.Auth.JWKSEndpoint,
		ClientCACert:  cfg.Auth.ClientCACert,
		RequireMTLS:   cfg.Auth.RequireMTLS,
		APIKeyHeader:  cfg.Auth.APIKeyHeader,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise auth middleware")
	}

	// ── HTTP server ───────────────────────────────────────────────────────
	router := rbehttpapi.NewRouter(svcHTTP, authMw)
	httpSrv := &http.Server{
		Addr:         cfg.Server.HTTPAddr,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// ── gRPC server ───────────────────────────────────────────────────────
	grpcSrv := grpc.New(svcGRPC, authMw)

	// ── Prometheus metrics endpoint ───────────────────────────────────────
	var metricsSrv *http.Server
	if cfg.Observability.PrometheusAddr != "" && cfg.Observability.PrometheusAddr != cfg.Server.HTTPAddr {
		metricsSrv = &http.Server{
			Addr:    cfg.Observability.PrometheusAddr,
			Handler: observability.MetricsHandler(),
		}
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	g, gCtx := errgroup.WithContext(ctx)

	// HTTP
	g.Go(func() error {
		log.Info().Str("addr", cfg.Server.HTTPAddr).Msg("HTTP server listening")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})

	// gRPC
	g.Go(func() error {
		log.Info().Str("addr", cfg.Server.GRPCAddr).Msg("gRPC server listening")
		return grpcSrv.Serve(cfg.Server.GRPCAddr)
	})

	// Prometheus (separate port if configured)
	if metricsSrv != nil {
		g.Go(func() error {
			log.Info().Str("addr", cfg.Observability.PrometheusAddr).Msg("metrics server listening")
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("metrics: %w", err)
			}
			return nil
		})
	}

	// Signal handler
	g.Go(func() error {
		select {
		case sig := <-sigCh:
			log.Info().Str("signal", sig.String()).Msg("shutting down")
		case <-gCtx.Done():
		}

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer shutdownCancel()

		grpcSrv.GracefulStop()
		httpSrv.Shutdown(shutdownCtx) //nolint:errcheck
		if metricsSrv != nil {
			metricsSrv.Shutdown(shutdownCtx) //nolint:errcheck
		}
		cancel()
		return nil
	})

	if err := g.Wait(); err != nil {
		log.Error().Err(err).Msg("server exited with error")
		os.Exit(1)
	}
	log.Info().Msg("shutdown complete")
}
