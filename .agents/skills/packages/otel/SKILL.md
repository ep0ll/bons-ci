---
name: pkg-otel
description: >
  Exhaustive reference for go.opentelemetry.io/otel: tracer setup, span creation, attributes,
  events, status codes, context propagation, baggage, metric instruments, OTLP exporter,
  sampling strategies, and correlation with logs. Primary observability SDK. Cross-references:
  observability/SKILL.md, packages/slog/SKILL.md, packages/grpc/SKILL.md.
---

# Package: go.opentelemetry.io/otel — Complete Reference

## Import
```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
    "go.opentelemetry.io/otel/trace"
    "go.opentelemetry.io/otel/metric"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
    "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
    "go.opentelemetry.io/otel/sdk/trace"
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
    "go.opentelemetry.io/otel/sdk/resource"
    semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)
```

## 1. SDK Initialization

```go
// Call once in main — returns cleanup func
func InitTelemetry(ctx context.Context, cfg TelemetryConfig) (func(context.Context) error, error) {
    res, err := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceName(cfg.ServiceName),
            semconv.ServiceVersion(cfg.ServiceVersion),
            semconv.DeploymentEnvironment(cfg.Environment),  // "production", "staging"
        ),
    )
    if err != nil { return nil, fmt.Errorf("otel.resource: %w", err) }

    // Trace exporter (OTLP over HTTP)
    traceExp, err := otlptracehttp.New(ctx,
        otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
        otlptracehttp.WithInsecure(), // remove in production
    )
    if err != nil { return nil, fmt.Errorf("otel.traceExporter: %w", err) }

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(traceExp),
        sdktrace.WithResource(res),
        sdktrace.WithSampler(
            sdktrace.ParentBased(
                sdktrace.TraceIDRatioBased(cfg.SampleRate), // e.g., 0.1 = 10%
            ),
        ),
    )
    otel.SetTracerProvider(tp)

    // Metric exporter (OTLP over HTTP)
    metricExp, err := otlpmetrichttp.New(ctx,
        otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint),
    )
    if err != nil { return nil, fmt.Errorf("otel.metricExporter: %w", err) }

    mp := sdkmetric.NewMeterProvider(
        sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
            sdkmetric.WithInterval(30*time.Second),
        )),
        sdkmetric.WithResource(res),
    )
    otel.SetMeterProvider(mp)

    // Set global propagator (trace context + baggage)
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{},
        propagation.Baggage{},
    ))

    return func(ctx context.Context) error {
        ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
        defer cancel()
        var errs []error
        if err := tp.Shutdown(ctx); err != nil { errs = append(errs, err) }
        if err := mp.Shutdown(ctx); err != nil { errs = append(errs, err) }
        return errors.Join(errs...)
    }, nil
}
```

## 2. Tracing — Spans

```go
// Package-level tracer — one per package, named with package import path
var tracer = otel.Tracer("github.com/org/project/internal/app/order")

func (h *CreateOrderHandler) Handle(ctx context.Context, cmd CreateOrderCommand) (*Order, error) {
    ctx, span := tracer.Start(ctx, "CreateOrderHandler.Handle",
        trace.WithAttributes(
            attribute.String("customer.id", cmd.CustomerID),
            attribute.Int("items.count", len(cmd.Items)),
        ),
        trace.WithSpanKind(trace.SpanKindInternal), // Internal | Server | Client | Producer | Consumer
    )
    defer span.End()

    order, err := h.createOrder(ctx, cmd)
    if err != nil {
        // ALWAYS record errors on spans
        span.RecordError(err,
            trace.WithAttributes(attribute.String("cmd.customer_id", cmd.CustomerID)),
        )
        span.SetStatus(codes.Error, err.Error())
        return nil, err
    }

    // Add result attributes
    span.SetAttributes(attribute.String("order.id", order.ID()))
    span.SetStatus(codes.Ok, "")
    return order, nil
}

// Add event (point-in-time annotation — not a sub-span)
span.AddEvent("inventory.checked", trace.WithAttributes(
    attribute.Bool("available", true),
    attribute.Int("reserved_count", len(items)),
))

// Link spans (for async/message-driven scenarios)
spanCtx := trace.SpanContextFromContext(publishCtx)
ctx, span = tracer.Start(ctx, "consumer.handle",
    trace.WithLinks(trace.Link{SpanContext: spanCtx}),
    trace.WithSpanKind(trace.SpanKindConsumer),
)
```

## 3. Attribute Naming Conventions

```go
// Use semantic conventions for well-known attributes
semconv.DBSystemPostgreSQL                   // db.system = "postgresql"
semconv.DBNameKey.String("orders")          // db.name
semconv.DBStatementKey.String(query)        // db.statement (sanitized — no values)
semconv.HTTPMethodKey.String("POST")        // http.method
semconv.HTTPStatusCodeKey.Int(200)          // http.status_code
semconv.NetPeerNameKey.String("localhost")  // net.peer.name
semconv.MessagingSystemKey.String("kafka")  // messaging.system
semconv.MessagingDestinationNameKey.String("orders") // messaging.destination.name

// Custom attributes: use dot-notation, domain-specific prefix
attribute.String("order.id", orderID)
attribute.String("order.status", string(status))
attribute.String("customer.id", customerID)
attribute.Int64("order.total_cents", totalCents)
attribute.Bool("order.is_first", isFirstOrder)
```

## 4. Metrics

```go
var meter = otel.Meter("github.com/org/project/internal/adapters/http")

// Counter — monotonically increasing
var requestCounter metric.Int64Counter
func init() {
    var err error
    requestCounter, err = meter.Int64Counter("http.requests.total",
        metric.WithDescription("Total HTTP requests"),
        metric.WithUnit("{request}"),
    )
    if err != nil { panic(err) }
}

// Histogram — distribution of values (latency, sizes)
var requestDuration metric.Float64Histogram
func init() {
    requestDuration, _ = meter.Float64Histogram("http.request.duration",
        metric.WithDescription("HTTP request duration"),
        metric.WithUnit("s"),
        metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10),
    )
}

// Gauge — current value (not monotonic)
var activeConnections metric.Int64UpDownCounter
func init() {
    activeConnections, _ = meter.Int64UpDownCounter("http.connections.active",
        metric.WithDescription("Active HTTP connections"),
    )
}

// Record observations with attributes
func (m *MetricsMiddleware) Handler(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        rw := wrapResponseWriter(w)
        next.ServeHTTP(rw, r)

        attrs := metric.WithAttributes(
            attribute.String("http.method", r.Method),
            attribute.String("http.route", chi.RouteContext(r.Context()).RoutePattern()),
            attribute.Int("http.status_code", rw.status),
        )
        requestCounter.Add(r.Context(), 1, attrs)
        requestDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)
    })
}
```

## 5. Context Propagation (HTTP)

```go
// Inject trace context into outgoing HTTP request
req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

// Extract trace context from incoming HTTP request
ctx = otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
```

## OTel Checklist
- [ ] `InitTelemetry` called once in `main` — shutdown func deferred
- [ ] Package-level `tracer` declared per package — not per function
- [ ] All significant functions create a span with `tracer.Start`
- [ ] `defer span.End()` immediately after `tracer.Start`
- [ ] `span.RecordError(err)` + `span.SetStatus(codes.Error)` on ALL errors
- [ ] `span.SetStatus(codes.Ok, "")` on success
- [ ] DB queries use `semconv.DBStatementKey` (sanitized — no parameter values)
- [ ] Metrics: counter for request count, histogram for duration, gauge for active
- [ ] HTTP propagation: inject on outgoing, extract on incoming
- [ ] Sampling configured: `TraceIDRatioBased(0.1)` for 10% in production
