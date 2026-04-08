// Command signing-service is the main entry point for the container image
// signing service. It wires all dependencies, starts the application, and
// handles graceful shutdown via OS signals.
//
// Production deployment:
//   - Run as a non-root user (UID 65532) in a distroless container
//   - Mount signing keys via Kubernetes projected volumes or GCP Secret Manager
//   - Set CONFIG_PATH to the mounted config file path
//   - Provide SIGTERM budget ≥ shutdown_timeout for graceful drain
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/bootstrap"
	"github.com/bons/bons-ci/pkg/sigstore/internal/config"
)

const (
	// defaultShutdownBudget is the fallback graceful shutdown budget.
	// Override via config.yaml server.shutdown_timeout.
	defaultShutdownBudget = 30 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// ── Load configuration ───────────────────────────────────────────────────
	// Fail fast: bad config means the service should not start.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// ── Wire dependencies ────────────────────────────────────────────────────
	// ctx is only for the wiring phase (OTel SDK init, gRPC dial).
	// Do not pass a long-lived context to Wire.
	wireCtx, wireCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer wireCancel()

	app, err := bootstrap.Wire(wireCtx, cfg)
	if err != nil {
		return fmt.Errorf("wire dependencies: %w", err)
	}

	// ── Start application ────────────────────────────────────────────────────
	// The run context lives until a signal is received.
	runCtx, runCancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, // Kubernetes sends SIGTERM for graceful pod eviction
		syscall.SIGINT,  // Ctrl+C for local dev
		syscall.SIGHUP,  // reload signal (reserved for future config hot-reload)
	)
	defer runCancel()

	// Start in a goroutine so signal handling below is not blocked.
	startErrCh := make(chan error, 1)
	go func() {
		if err := app.Start(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			startErrCh <- fmt.Errorf("app start: %w", err)
		}
		close(startErrCh)
	}()

	// ── Wait for signal or startup failure ───────────────────────────────────
	select {
	case err := <-startErrCh:
		if err != nil {
			return err
		}
	case <-runCtx.Done():
		slog.Info("shutdown signal received", "signal", runCtx.Err())
	}

	// ── Graceful shutdown ────────────────────────────────────────────────────
	budget := cfg.Server.ShutdownTimeout
	if budget == 0 {
		budget = defaultShutdownBudget
	}
	shutCtx, shutCancel := context.WithTimeout(context.Background(), budget)
	defer shutCancel()

	if err := app.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	// Drain any remaining start error after shutdown (race between signal and error)
	if err, ok := <-startErrCh; ok && err != nil {
		return err
	}

	return nil
}
