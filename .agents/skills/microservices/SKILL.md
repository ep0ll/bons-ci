---
name: golang-microservices
description: >
  Go microservices architecture: service boundaries, inter-service communication patterns,
  service mesh integration (Istio/Linkerd), service discovery, distributed tracing across
  services, API gateways, backward compatibility, contract testing, and operational patterns
  (blue-green, canary). Cross-references: architecture/SKILL.md, networking/SKILL.md,
  event-driven/SKILL.md, deployment/SKILL.md, resilience/SKILL.md.
---

# Go Microservices — Production Patterns

## 1. Service Boundary Design

```
// Service decomposition principles:
//
// ✓ Split by business capability (not technical layer)
//   order-service, payment-service, notification-service
//   NOT: persistence-service, validation-service
//
// ✓ Each service owns its data — no shared databases
//   order-service → orders DB
//   payment-service → payments DB
//   Never: two services writing to same table
//
// ✓ Loose coupling via events, not direct DB joins
//   order-service publishes order.created event
//   fulfillment-service subscribes, has own copy of needed order data
//
// ✓ High cohesion — all order logic lives in order-service
//   Not scattered across services
//
// Question to ask: "Can this service be deployed independently?"
// If no → it's not a real microservice yet
```

## 2. gRPC Service Client Pattern

```go
// Typed client wrapper with resilience built in
type OrderServiceClient struct {
    conn    *grpc.ClientConn
    client  orderv1.OrderServiceClient
    breaker *gobreaker.CircuitBreaker
    logger  *slog.Logger
}

func NewOrderServiceClient(ctx context.Context, cfg ClientConfig) (*OrderServiceClient, error) {
    conn, err := grpc.DialContext(ctx, cfg.Addr,
        grpc.WithTransportCredentials(credentials.NewTLS(cfg.TLS)),
        grpc.WithKeepaliveParams(keepalive.ClientParameters{
            Time: 10 * time.Second, Timeout: 5 * time.Second,
        }),
        grpc.WithChainUnaryInterceptor(
            otelgrpc.UnaryClientInterceptor(),
            grpcRetryInterceptor(3),
        ),
    )
    if err != nil { return nil, fmt.Errorf("dial order-service: %w", err) }

    return &OrderServiceClient{
        conn:   conn,
        client: orderv1.NewOrderServiceClient(conn),
        breaker: gobreaker.NewCircuitBreaker(gobreaker.Settings{
            Name: "order-service", Timeout: 30 * time.Second,
        }),
    }, nil
}

func (c *OrderServiceClient) GetOrder(ctx context.Context, id string) (*Order, error) {
    ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
    defer cancel()

    result, err := c.breaker.Execute(func() (any, error) {
        resp, err := c.client.GetOrder(ctx, &orderv1.GetOrderRequest{Id: id})
        if err != nil { return nil, err }
        return fromProtoOrder(resp), nil
    })
    if err != nil {
        if errors.Is(err, gobreaker.ErrOpenState) {
            return nil, fmt.Errorf("order-service unavailable: %w", domain.ErrServiceUnavailable)
        }
        return nil, fmt.Errorf("GetOrder(%s): %w", id, err)
    }
    return result.(*Order), nil
}

func (c *OrderServiceClient) Close() error { return c.conn.Close() }
```

## 3. Async Inter-Service Communication

```go
// Prefer async (event-driven) for:
//   - Non-time-critical workflows
//   - Operations that can tolerate eventual consistency
//   - Fan-out (one event, many consumers)
//   - Decoupling service lifecycles

// Use sync (gRPC/HTTP) for:
//   - User-facing real-time requests
//   - Operations requiring immediate consistency
//   - Query/read operations

// Anti-pattern: sync chain of 5+ services for one user request
// → user waits for all, failure of any fails all
// Fix: complete user request with available data, async enrich

// Pattern: saga for distributed transactions (see event-driven/SKILL.md)
```

## 4. Service-to-Service Authentication (mTLS + JWT)

```go
// Every service-to-service call must be authenticated
// Option 1: mTLS (mutual TLS) — network-level, handled by service mesh
// Option 2: Service JWT — application-level, explicit

// Service JWT: service presents its own JWT (not user's JWT)
type ServiceIdentity struct {
    ServiceName string
    privateKey  *rsa.PrivateKey
}

func (s *ServiceIdentity) GenerateServiceToken() (string, error) {
    claims := jwt.RegisteredClaims{
        Issuer:    s.ServiceName,
        Subject:   s.ServiceName,
        Audience:  jwt.ClaimStrings{"internal"},
        IssuedAt:  jwt.NewNumericDate(time.Now()),
        ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
        ID:        uuid.New().String(),
    }
    token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
    return token.SignedString(s.privateKey)
}

// Inject service token in outgoing gRPC calls
func serviceTokenInterceptor(identity *ServiceIdentity) grpc.UnaryClientInterceptor {
    return func(ctx context.Context, method string, req, reply any,
        cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {

        token, err := identity.GenerateServiceToken()
        if err != nil { return fmt.Errorf("service token: %w", err) }
        ctx = metadata.AppendToOutgoingContext(ctx, "x-service-token", token)
        return invoker(ctx, method, req, reply, cc, opts...)
    }
}
```

## 5. Distributed Tracing (Cross-Service)

```go
// Trace context MUST be propagated across service boundaries
// gRPC: otelgrpc interceptors handle this automatically
// HTTP: inject/extract W3C Trace Context headers

// HTTP client: inject outgoing trace context
func newInstrumentedHTTPClient() *http.Client {
    return &http.Client{
        Transport: otelhttp.NewTransport(http.DefaultTransport),
        // otelhttp.NewTransport automatically:
        // - Creates a client span for each request
        // - Injects traceparent/tracestate headers
        Timeout: 30 * time.Second,
    }
}

// HTTP server: extract incoming trace context
mux.Handle("/", otelhttp.NewHandler(handler, "order-service",
    otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
))
```

## 6. Contract Testing (Consumer-Driven)

```go
// Pact: consumer defines what it expects from provider
// Provider must satisfy all registered contracts
// Prevents breaking changes between services

// Consumer test (order-service consuming inventory-service)
func TestInventoryClientContract(t *testing.T) {
    // Define what we expect inventory-service to return
    pact := dsl.Pact{
        Consumer: "order-service",
        Provider: "inventory-service",
    }
    defer pact.Teardown()

    pact.AddInteraction().
        Given("Product 123 has stock").
        UponReceiving("a stock check for product 123").
        WithRequest(dsl.Request{
            Method: "GET",
            Path:   dsl.String("/v1/products/123/stock"),
        }).
        WillRespondWith(dsl.Response{
            Status: 200,
            Body:   dsl.Match(StockResponse{Available: true, Count: 50}),
        })

    pact.Verify(t, func() error {
        client := NewInventoryClient(pact.MockServer.URL)
        stock, err := client.CheckStock(context.Background(), "123")
        assert.NoError(t, err)
        assert.True(t, stock.Available)
        return nil
    })
}
```

## 7. Health Aggregation (Dependency Health)

```go
// Readiness probe checks all critical downstream dependencies
type ServiceHealthChecker struct {
    checks []HealthCheck
}

type HealthCheck struct {
    Name     string
    Critical bool   // if true, failure marks service as not ready
    Check    func(ctx context.Context) error
}

func (h *ServiceHealthChecker) Ready(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
    defer cancel()

    type result struct {
        Status string `json:"status"`
        Error  string `json:"error,omitempty"`
    }
    results := make(map[string]result)
    healthy := true

    for _, check := range h.checks {
        err := check.Check(ctx)
        if err != nil {
            results[check.Name] = result{Status: "unhealthy", Error: err.Error()}
            if check.Critical { healthy = false }
        } else {
            results[check.Name] = result{Status: "healthy"}
        }
    }

    status := http.StatusOK
    if !healthy { status = http.StatusServiceUnavailable }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(map[string]any{
        "status": map[bool]string{true: "ready", false: "not_ready"}[healthy],
        "checks": results,
    })
}
```

## Microservices Checklist
- [ ] Service owns its own database — no cross-service DB access
- [ ] Sync communication (gRPC) for real-time user-facing ops
- [ ] Async communication (events) for background/decoupled workflows
- [ ] Circuit breaker on all inter-service calls
- [ ] Service-level JWT or mTLS for all inter-service auth
- [ ] Distributed tracing headers propagated (otelhttp/otelgrpc)
- [ ] Contract tests (Pact) for service API contracts
- [ ] Readiness probe checks all critical downstream deps
- [ ] Graceful degradation when non-critical dependencies are unavailable
- [ ] Service versioning in URL path (/v1/) or proto package (order.v1)
- [ ] Each service deployable independently (no deploy-together coupling)
