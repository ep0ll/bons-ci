// Package telemetry provides Prometheus metrics and structured health
// reporting for the live migrator.
package telemetry

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/oracle/internal/config"
)

// Provider holds all telemetry subsystems.
type Provider struct {
	metrics *Metrics
	srv     *http.Server
	log     *zap.Logger
}

// Metrics contains all Prometheus counters/gauges/histograms.
type Metrics struct {
	PreemptionNoticesTotal prometheus.Counter
	MigrationSuccessTotal  prometheus.Counter
	MigrationFailuresTotal prometheus.Counter
	MetadataFetchErrors    prometheus.Counter

	CheckpointDurationSeconds prometheus.Histogram
	FreezeDurationSeconds     prometheus.Histogram
	RestoreDurationSeconds    prometheus.Histogram
	TotalMigrationSeconds     prometheus.Histogram

	CheckpointMemoryBytes prometheus.Gauge
	CheckpointPIDCount    prometheus.Gauge
	MigrationPhase        *prometheus.GaugeVec
}

// New constructs and registers all metrics, then starts the Prometheus HTTP server.
func New(cfg config.TelemetryConfig) (*Provider, error) {
	reg := prometheus.DefaultRegisterer

	m := &Metrics{
		PreemptionNoticesTotal: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Namespace: "oci_migrator",
			Name:      "preemption_notices_total",
			Help:      "Total preemption notices received from OCI IMDS.",
		}),
		MigrationSuccessTotal: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Namespace: "oci_migrator",
			Name:      "migration_success_total",
			Help:      "Total successful live migrations.",
		}),
		MigrationFailuresTotal: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Namespace: "oci_migrator",
			Name:      "migration_failures_total",
			Help:      "Total failed live migrations.",
		}),
		MetadataFetchErrors: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Namespace: "oci_migrator",
			Name:      "metadata_fetch_errors_total",
			Help:      "Total OCI IMDS fetch errors.",
		}),
		CheckpointDurationSeconds: promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
			Namespace: "oci_migrator",
			Name:      "checkpoint_duration_seconds",
			Help:      "Total CRIU checkpoint duration.",
			Buckets:   []float64{1, 2, 5, 10, 20, 30, 60},
		}),
		FreezeDurationSeconds: promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
			Namespace: "oci_migrator",
			Name:      "freeze_duration_seconds",
			Help:      "Process freeze window during final CRIU dump.",
			Buckets:   []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}),
		RestoreDurationSeconds: promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
			Namespace: "oci_migrator",
			Name:      "restore_duration_seconds",
			Help:      "CRIU restore duration on successor.",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10},
		}),
		TotalMigrationSeconds: promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
			Namespace: "oci_migrator",
			Name:      "total_migration_seconds",
			Help:      "Wall-clock time for the full source-side migration pipeline.",
			Buckets:   []float64{10, 20, 30, 45, 60, 90, 120},
		}),
		CheckpointMemoryBytes: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Namespace: "oci_migrator",
			Name:      "checkpoint_memory_bytes",
			Help:      "Size of the last CRIU checkpoint image in bytes.",
		}),
		CheckpointPIDCount: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Namespace: "oci_migrator",
			Name:      "checkpoint_pid_count",
			Help:      "Number of PIDs in the last CRIU checkpoint.",
		}),
		MigrationPhase: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "oci_migrator",
			Name:      "migration_phase",
			Help:      "Current migration phase (1 = active).",
		}, []string{"phase"}),
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	srv := &http.Server{
		Addr:    cfg.PrometheusAddr,
		Handler: mux,
	}

	log, _ := zap.NewProduction()
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server error", zap.Error(err))
		}
	}()
	log.Info("metrics server started", zap.String("addr", cfg.PrometheusAddr))

	return &Provider{metrics: m, srv: srv, log: log}, nil
}

// Metrics returns the metrics registry.
func (p *Provider) Metrics() *Metrics {
	return p.metrics
}

// Shutdown gracefully stops the HTTP server.
func (p *Provider) Shutdown(ctx context.Context) error {
	return p.srv.Shutdown(ctx)
}
