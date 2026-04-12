// Package observability wires Prometheus metrics, structured slog logging,
// and OpenTelemetry tracing into a single injectable bundle.
//
// Design: a single *Metrics struct is passed through the dependency graph
// (not a global variable). Each component registers only the metrics it owns.
// This makes metrics ownership explicit and prevents label collisions.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const namespace = "signing_service"

// Metrics holds all Prometheus instruments for the service.
// Each field maps to one logical metric with labelled variants.
type Metrics struct {
	// Signing
	SigningDuration *prometheus.HistogramVec // labels: signer_type, outcome
	SigningTotal    *prometheus.CounterVec   // labels: signer_type, outcome

	// Event bus
	EventsPublished *prometheus.CounterVec // labels: topic
	EventsDropped   *prometheus.CounterVec // labels: topic

	// Resilience
	RetryCount          *prometheus.CounterVec // labels: result (retry|exhausted)
	CircuitBreakerState *prometheus.GaugeVec   // labels: name, state

	// Idempotency
	IdempotencyHits *prometheus.CounterVec // labels: result (claimed|duplicate|error)

	// HTTP (health + API)
	HTTPRequestDuration *prometheus.HistogramVec // labels: handler, method, status_code
}

// NewMetrics registers all metrics with the given Prometheus registerer.
// Passing a custom registerer (e.g. prometheus.NewRegistry()) enables isolated
// tests without affecting the default global registry.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		SigningDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "signing_duration_seconds",
			Help:      "End-to-end signing latency including Rekor attachment.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"signer_type", "outcome"}),

		SigningTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "signing_total",
			Help:      "Total signing attempts by type and outcome.",
		}, []string{"signer_type", "outcome"}),

		EventsPublished: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_published_total",
			Help:      "Events successfully enqueued by topic.",
		}, []string{"topic"}),

		EventsDropped: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_dropped_total",
			Help:      "Events dropped due to back-pressure by topic.",
		}, []string{"topic"}),

		RetryCount: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "resilience_retry_total",
			Help:      "Retry attempts classified by result.",
		}, []string{"result"}),

		CircuitBreakerState: factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "circuit_breaker_state",
			Help:      "Circuit breaker state: 1 = active state, 0 = inactive.",
		}, []string{"name", "state"}),

		IdempotencyHits: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "idempotency_hits_total",
			Help:      "Idempotency store outcomes by result.",
		}, []string{"result"}),

		HTTPRequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP handler latency.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"handler", "method", "status_code"}),
	}
}

// --- Structured logging ─────────────────────────────────────────────────────

// LogConfig controls slog output format and level.
type LogConfig struct {
	Level  slog.Level
	Format string // "json" | "text"
}

// NewLogger creates a levelled, optionally JSON slog.Logger.
// JSON format is strongly recommended for production (Datadog, CloudWatch, etc.).
func NewLogger(cfg LogConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}
	var handler slog.Handler
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// --- OpenTelemetry tracing ──────────────────────────────────────────────────

// TracingConfig holds OTel SDK configuration.
type TracingConfig struct {
	ServiceName    string
	ServiceVersion string
	// OTLPEndpoint is the gRPC endpoint for the OTLP exporter.
	// Example: "otel-collector:4317"
	// Leave empty to disable tracing (NoopTracerProvider is used).
	OTLPEndpoint string
}

// TracerProvider wraps the OTel SDK provider and exposes a clean shutdown.
type TracerProvider struct {
	provider *sdktrace.TracerProvider
}

// Tracer returns a named tracer for the given instrumentation scope.
func (tp *TracerProvider) Tracer(name string) trace.Tracer {
	if tp.provider == nil {
		return otel.GetTracerProvider().Tracer(name)
	}
	return tp.provider.Tracer(name)
}

// Shutdown flushes pending spans and releases exporter resources.
func (tp *TracerProvider) Shutdown(ctx context.Context) error {
	if tp.provider == nil {
		return nil
	}
	return tp.provider.Shutdown(ctx)
}

// NewTracerProvider initialises the OTel SDK with a gRPC OTLP exporter.
// If cfg.OTLPEndpoint is empty, a no-op provider is returned so tracing
// can be disabled in environments without a collector.
func NewTracerProvider(ctx context.Context, cfg TracingConfig) (*TracerProvider, error) {
	if cfg.OTLPEndpoint == "" {
		return &TracerProvider{}, nil
	}

	conn, err := grpc.NewClient(cfg.OTLPEndpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("otel grpc dial %q: %w", cfg.OTLPEndpoint, err)
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("otel exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	return &TracerProvider{provider: provider}, nil
}
