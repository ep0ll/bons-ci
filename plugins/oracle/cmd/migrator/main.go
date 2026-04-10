// Package main is the entry point for the OCI Preemptible VM Live Migration Daemon.
// v2: warm pool, adaptive polling, circuit breakers, page-server, zstd compression.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/oracle/internal/config"
	"github.com/bons/bons-ci/plugins/oracle/internal/migration"
	"github.com/bons/bons-ci/plugins/oracle/internal/monitor"
	"github.com/bons/bons-ci/plugins/oracle/internal/oci"
	"github.com/bons/bons-ci/plugins/oracle/internal/state"
	"github.com/bons/bons-ci/plugins/oracle/internal/telemetry"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	log, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: logger init: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	log.Info("OCI Preemptible Live Migrator v2",
		zap.String("version", Version),
		zap.String("built", BuildTime),
		zap.Int("pid", os.Getpid()),
	)

	cfg, err := config.Load()
	if err != nil {
		log.Fatal("config load failed", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	tel, err := telemetry.New(cfg.Telemetry)
	if err != nil {
		log.Fatal("telemetry init", zap.Error(err))
	}
	defer tel.Shutdown(context.Background()) //nolint:errcheck

	ociSession, err := oci.NewSession(cfg.OCI, log)
	if err != nil {
		log.Fatal("OCI session init", zap.Error(err))
	}

	sm, err := state.NewManager(cfg.State, log)
	if err != nil {
		log.Fatal("state manager init", zap.Error(err))
	}

	orch, err := migration.NewOrchestrator(migration.OrchestratorConfig{
		OCI:       ociSession,
		State:     sm,
		Config:    cfg,
		Log:       log,
		Telemetry: tel,
	})
	if err != nil {
		log.Fatal("orchestrator init", zap.Error(err))
	}

	// ── Successor mode: restore checkpointed processes then exit ─────────
	if cfg.Migration.IsSuccessor {
		log.Info("running in successor/restore mode")
		if err := orch.Restore(ctx); err != nil {
			log.Fatal("restore failed", zap.Error(err))
		}
		log.Info("restore complete")
		return
	}

	// ── Source mode: warm pool + monitoring loop ──────────────────────────
	// Pre-cache identity and pre-heat warm pool in background.
	orch.StartBackground(ctx)

	pm := monitor.NewPreemptionMonitor(monitor.Config{
		MetadataURL:      cfg.OCI.MetadataEndpoint,
		PollInterval:     cfg.Migration.MonitorInterval,
		FastPollInterval: cfg.Migration.FastMonitorInterval,
		MigrationBudget:  cfg.Migration.MigrationBudget,
		Log:              log,
		Metrics:          tel.Metrics(),
	})

	log.Info("entering monitoring loop",
		zap.Duration("poll_interval", cfg.Migration.MonitorInterval),
		zap.Bool("warm_pool_enabled", cfg.WarmPool.Enabled),
		zap.Int("page_server_port", cfg.CRIU.PageServerPort),
	)

	pm.Start(ctx)

	for {
		select {
		case evt := <-pm.Events():
			switch evt.Type {
			case monitor.EventPreemptionNotice:
				tel.Metrics().PreemptionNoticesTotal.Inc()
				log.Warn("PREEMPTION NOTICE received",
					zap.Time("termination_time", evt.TerminationTime),
					zap.Duration("remaining", time.Until(evt.TerminationTime)),
					zap.String("detection_source", evt.Source),
				)

				migCtx, migCancel := context.WithDeadline(
					ctx, evt.TerminationTime.Add(-10*time.Second),
				)

				if err := orch.Migrate(migCtx, evt); err != nil {
					log.Error("MIGRATION FAILED",
						zap.Error(err),
						zap.Duration("remaining", time.Until(evt.TerminationTime)),
					)
					tel.Metrics().MigrationFailuresTotal.Inc()
					_ = orch.EmergencyCheckpoint(ctx)
				} else {
					tel.Metrics().MigrationSuccessTotal.Inc()
					log.Info("migration complete")
				}
				migCancel()

			case monitor.EventHeartbeat:
				log.Debug("heartbeat")
			}

		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				log.Info("SIGHUP — no-op")
				continue
			}
			log.Info("shutdown", zap.String("signal", sig.String()))
			cancel()
			return

		case <-ctx.Done():
			return
		}
	}
}
