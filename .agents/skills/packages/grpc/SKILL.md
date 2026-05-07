---
name: pkg-grpc
description: >
  Exhaustive reference for google.golang.org/grpc: server setup with interceptors, client
  connection with retry/keepalive, streaming RPCs, error handling with status codes, metadata,
  health checking, reflection, load balancing, TLS/mTLS, and middleware patterns.
  Cross-references: networking/SKILL.md, security/SKILL.md, packages/protobuf/SKILL.md.
---

# Package: google.golang.org/grpc — Complete Reference

## Imports
```go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/credentials"
    "google.golang.org/grpc/credentials/insecure"
    "google.golang.org/grpc/keepalive"
    "google.golang.org/grpc/metadata"
    "google.golang.org/grpc/status"
    "google.golang.org/grpc/health/grpc_health_v1"
    "google.golang.org/grpc/reflection"
    "go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
)
```

## 1. Server Setup

```go
func NewGRPCServer(cfg ServerConfig, svc OrderServiceServer) (*grpc.Server, error) {
    tlsCfg, err := loadTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
    if err != nil { return nil, fmt.Errorf("NewGRPCServer.TLS: %w", err) }

    srv := grpc.NewServer(
        grpc.Creds(credentials.NewTLS(tlsCfg)),

        // Keepalive: prevent dead connections from accumulating
        grpc.KeepaliveParams(keepalive.ServerParameters{
            MaxConnectionIdle:     15 * time.Minute,
            MaxConnectionAge:      30 * time.Minute,
            MaxConnectionAgeGrace: 5 * time.Second,
            Time:                  5 * time.Second,   // ping if idle
            Timeout:               1 * time.Second,   // ping timeout
        }),
        grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
            MinTime:             5 * time.Second,
            PermitWithoutStream: true,
        }),

        // Message size limits
        grpc.MaxRecvMsgSize(4 << 20),  // 4 MB
        grpc.MaxSendMsgSize(4 << 20),

        // Interceptors (applied in order)
        grpc.ChainUnaryInterceptor(
            otelgrpc.UnaryServerInterceptor(),   // tracing
            grpcLoggingInterceptor(logger),      // structured logging
            grpcRecoveryInterceptor(),           // panic recovery
            grpcAuthInterceptor(tokenValidator), // auth
            grpcValidationInterceptor(),         // proto-validate
        ),
        grpc.ChainStreamInterceptor(
            otelgrpc.StreamServerInterceptor(),
            grpcStreamLoggingInterceptor(logger),
            grpcStreamRecoveryInterceptor(),
        ),
    )

    // Register services
    pb.RegisterOrderServiceServer(srv, svc)

    // Health check (required for k8s liveness/readiness probes)
    healthSrv := health.NewServer()
    grpc_health_v1.RegisterHealthServer(srv, healthSrv)
    healthSrv.SetServingStatus("order.v1.OrderService", grpc_health_v1.HealthCheckResponse_SERVING)

    // Reflection (enables grpcurl, grpcui in dev/staging)
    if cfg.EnableReflection {
        reflection.Register(srv)
    }

    return srv, nil
}

// Start + graceful shutdown
func ServeGRPC(ctx context.Context, srv *grpc.Server, addr string) error {
    lis, err := net.Listen("tcp", addr)
    if err != nil { return fmt.Errorf("gRPC.Listen(%s): %w", addr, err) }

    errCh := make(chan error, 1)
    go func() {
        if err := srv.Serve(lis); err != nil { errCh <- err }
    }()

    select {
    case <-ctx.Done():
        srv.GracefulStop()  // drains in-flight RPCs
        return nil
    case err := <-errCh:
        return fmt.Errorf("gRPC.Serve: %w", err)
    }
}
```

## 2. Client Connection

```go
func NewGRPCClient(ctx context.Context, addr string, tlsCfg *tls.Config) (*grpc.ClientConn, error) {
    creds := credentials.NewTLS(tlsCfg)
    // For dev/test only:
    // creds := insecure.NewCredentials()

    conn, err := grpc.DialContext(ctx, addr,
        grpc.WithTransportCredentials(creds),
        grpc.WithKeepaliveParams(keepalive.ClientParameters{
            Time:                10 * time.Second,
            Timeout:             5 * time.Second,
            PermitWithoutStream: true,
        }),
        grpc.WithDefaultCallOptions(
            grpc.WaitForReady(true),              // retry on transient failures
            grpc.MaxCallRecvMsgSize(4<<20),
            grpc.MaxCallSendMsgSize(4<<20),
        ),
        grpc.WithChainUnaryInterceptor(
            otelgrpc.UnaryClientInterceptor(),
            grpcClientLoggingInterceptor(logger),
        ),
        grpc.WithChainStreamInterceptor(
            otelgrpc.StreamClientInterceptor(),
        ),
    )
    if err != nil { return nil, fmt.Errorf("grpc.Dial(%s): %w", addr, err) }
    return conn, nil
}
```

## 3. Interceptors

```go
// Logging interceptor
func grpcLoggingInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
        start := time.Now()
        resp, err := handler(ctx, req)
        st, _ := status.FromError(err)
        log.InfoContext(ctx, "grpc request",
            slog.String("method", info.FullMethod),
            slog.String("code", st.Code().String()),
            slog.Duration("duration", time.Since(start)),
        )
        return resp, err
    }
}

// Recovery interceptor
func grpcRecoveryInterceptor() grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
        defer func() {
            if p := recover(); p != nil {
                slog.ErrorContext(ctx, "grpc panic", "panic", p, "stack", string(debug.Stack()))
                err = status.Errorf(codes.Internal, "internal error")
            }
        }()
        return handler(ctx, req)
    }
}
```

## 4. Error Handling

```go
// ALWAYS return grpc/status errors from gRPC handlers — never raw errors
func (s *OrderServer) GetOrder(ctx context.Context, req *pb.GetOrderRequest) (*pb.Order, error) {
    if req.Id == "" {
        return nil, status.Error(codes.InvalidArgument, "id is required")
    }

    o, err := s.svc.GetOrder(ctx, req.Id)
    if err != nil {
        return nil, toGRPCStatus(err)  // domain error → gRPC status code
    }
    return toProtoOrder(o), nil
}

// Domain error → gRPC status mapping
func toGRPCStatus(err error) error {
    switch {
    case err == nil:                              return nil
    case errors.Is(err, domain.ErrNotFound):     return status.Error(codes.NotFound, "not found")
    case errors.Is(err, domain.ErrConflict):     return status.Error(codes.Aborted, "conflict")
    case errors.Is(err, domain.ErrUnauthorized): return status.Error(codes.Unauthenticated, "unauthenticated")
    case errors.Is(err, domain.ErrForbidden):    return status.Error(codes.PermissionDenied, "forbidden")
    case errors.Is(err, domain.ErrValidation):   return status.Error(codes.InvalidArgument, sanitizeValidationErr(err))
    case errors.Is(err, domain.ErrRateLimit):    return status.Error(codes.ResourceExhausted, "rate limited")
    case errors.Is(err, context.DeadlineExceeded): return status.Error(codes.DeadlineExceeded, "deadline exceeded")
    default:
        slog.Error("unhandled gRPC error", "err", err)  // log internal; never expose
        return status.Error(codes.Internal, "internal error")
    }
}

// Client: extract status from error
func handleClientError(err error) {
    st, ok := status.FromError(err)
    if !ok { /* not a gRPC error */ return }
    switch st.Code() {
    case codes.NotFound:          // handle 404
    case codes.Unavailable:       // retry with backoff
    case codes.ResourceExhausted: // rate limit — backoff
    }
}
```

## 5. Metadata (Headers)

```go
// Send metadata from client
ctx = metadata.AppendToOutgoingContext(ctx,
    "x-request-id", requestID,
    "authorization", "Bearer "+token,
)

// Read metadata in server
md, ok := metadata.FromIncomingContext(ctx)
if ok {
    requestIDs := md.Get("x-request-id")
    if len(requestIDs) > 0 { /* use requestIDs[0] */ }
}

// Send response headers from server (before first write)
header := metadata.Pairs("x-request-id", requestID)
grpc.SetHeader(ctx, header)
```

## 6. Streaming

```go
// Server streaming
func (s *OrderServer) WatchOrders(req *pb.WatchOrdersRequest, stream pb.OrderService_WatchOrdersServer) error {
    ch, cancel := s.events.Subscribe(req.CustomerId)
    defer cancel()

    for {
        select {
        case event, ok := <-ch:
            if !ok { return nil }
            if err := stream.Send(toProtoEvent(event)); err != nil {
                return fmt.Errorf("stream.Send: %w", err)  // client disconnected
            }
        case <-stream.Context().Done():
            return nil  // client cancelled
        }
    }
}
```

## gRPC Checklist
- [ ] TLS credentials on all production servers (never `insecure.NewCredentials()` in prod)
- [ ] Keepalive params configured on both client and server
- [ ] `GracefulStop()` used for shutdown — not `Stop()` (which kills in-flight RPCs)
- [ ] All handlers return `status.Error(code, msg)` — never raw errors
- [ ] Internal errors (5xx equiv) log full details but return `codes.Internal` with generic msg
- [ ] Health service registered for k8s probes
- [ ] Reflection enabled in dev/staging, disabled in production
- [ ] OTel interceptors registered on both client and server
- [ ] Message size limits set (default 4MB recv/send)
