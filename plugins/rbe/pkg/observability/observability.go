// Package observability sets up OpenTelemetry tracing, Prometheus metrics,
// and structured zerolog logging for the RBE server.
package observability

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// ─────────────────────────────────────────────────────────────────────────────
// Metrics
// ─────────────────────────────────────────────────────────────────────────────

var (
	// Registry metrics
	BlobUploads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rbe_blob_uploads_total",
		Help: "Total blob upload operations.",
	}, []string{"repo", "status"})

	BlobDownloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rbe_blob_downloads_total",
		Help: "Total blob download operations.",
	}, []string{"repo", "status"})

	BlobSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rbe_blob_size_bytes",
		Help:    "Size of blobs transferred.",
		Buckets: prometheus.ExponentialBuckets(1024, 4, 16),
	}, []string{"direction"})

	ManifestOps = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rbe_manifest_operations_total",
		Help: "Total manifest operations.",
	}, []string{"op", "repo", "status"})

	// DAG metrics
	DAGsCreated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "rbe_dags_created_total",
		Help: "Total DAGs created.",
	})

	VerticesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rbe_vertices_total",
		Help: "Total vertices processed.",
	}, []string{"status", "op_type"})

	VertexDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rbe_vertex_duration_seconds",
		Help:    "Wall-time duration of vertex execution.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 20),
	}, []string{"op_type", "status"})

	// Cache metrics
	CacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rbe_cache_hits_total",
		Help: "Total cache hits.",
	}, []string{"result"})

	// Log metrics
	LogChunksIngested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rbe_log_chunks_ingested_total",
		Help: "Total log chunks ingested.",
	}, []string{"fd_type"})

	LogBytesIngested = promauto.NewCounter(prometheus.CounterOpts{
		Name: "rbe_log_bytes_ingested_total",
		Help: "Total log bytes ingested.",
	})

	// HTTP metrics
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rbe_http_request_duration_seconds",
		Help:    "HTTP request duration by method and path.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status"})

	// gRPC metrics
	GRPCRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rbe_grpc_request_duration_seconds",
		Help:    "gRPC request duration by method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "status"})
)

// ─────────────────────────────────────────────────────────────────────────────
// Tracing
// ─────────────────────────────────────────────────────────────────────────────

// SetupTracing initialises the OpenTelemetry tracer with an OTLP gRPC exporter.
// Returns a shutdown function to be deferred in main.
func SetupTracing(ctx context.Context, otlpEndpoint, serviceName, serviceVersion string) (func(context.Context) error, error) {
	if otlpEndpoint == "" {
		// No-op tracer.
		otel.SetTracerProvider(otel.GetTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, _ := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			attribute.String("service.component", "rbed"),
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return func(ctx context.Context) error {
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx2)
	}, nil
}

// Tracer returns the named tracer for a subsystem.
func Tracer(name string) trace.Tracer {
	return otel.Tracer("github.com/bons/bons-ci/plugins/rbe/" + name)
}

// ─────────────────────────────────────────────────────────────────────────────
// Logging
// ─────────────────────────────────────────────────────────────────────────────

// SetupLogging configures zerolog global logger.
func SetupLogging(level string) {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)
	zerolog.TimeFieldFormat = time.RFC3339Nano
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
	}).With().Str("service", "rbed").Caller().Logger()
}

// Logger returns the global zerolog logger.
func Logger() zerolog.Logger { return log.Logger }

// ─────────────────────────────────────────────────────────────────────────────
// Prometheus HTTP handler
// ─────────────────────────────────────────────────────────────────────────────

// MetricsHandler returns the Prometheus metrics HTTP handler.
func MetricsHandler() http.Handler { return promhttp.Handler() }

// ─────────────────────────────────────────────────────────────────────────────
// HTTP instrumentation middleware
// ─────────────────────────────────────────────────────────────────────────────

// HTTPMiddleware records request duration and emits trace spans.
func HTTPMiddleware(next http.Handler) http.Handler {
	tracer := Tracer("http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path)
		defer span.End()

		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r.WithContext(ctx))

		dur := time.Since(start).Seconds()
		statusStr := http.StatusText(rw.status)
		HTTPRequestDuration.WithLabelValues(r.Method, r.URL.Path, statusStr).Observe(dur)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}
