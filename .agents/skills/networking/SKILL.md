---
name: golang-networking
description: >
  Go networking: TCP/UDP servers and clients, HTTP/2, gRPC, WebSockets, TLS, DNS resolution,
  connection pooling, load balancing, circuit breakers, retry logic, eBPF networking hooks,
  raw socket programming, and network namespace manipulation. Use for any Go code doing
  network I/O, building servers, clients, proxies, or network infrastructure.
---

# Go Networking — Production Patterns

## 1. HTTP Server — Hardened Production Config

```go
func NewHTTPServer(handler http.Handler, opts ...ServerOption) *http.Server {
    cfg := &serverConfig{
        addr:         ":8080",
        readTimeout:  15 * time.Second,
        writeTimeout: 15 * time.Second,
        idleTimeout:  120 * time.Second,
        maxHeaderBytes: 1 << 20, // 1 MB
    }
    for _, o := range opts { o(cfg) }
    
    return &http.Server{
        Addr:           cfg.addr,
        Handler:        handler,
        ReadTimeout:    cfg.readTimeout,
        WriteTimeout:   cfg.writeTimeout,
        IdleTimeout:    cfg.idleTimeout,
        MaxHeaderBytes: cfg.maxHeaderBytes,
        // Always: disable default mux, use structured logger
        ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
        // TLS from security/SKILL.md when HTTPS
    }
}

// Middleware chain (composable)
func Chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
    for i := len(middlewares) - 1; i >= 0; i-- {
        h = middlewares[i](h)
    }
    return h
}

// Essential middleware: logging, recovery, request ID
func RequestID(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := r.Header.Get("X-Request-ID")
        if id == "" {
            b := make([]byte, 16)
            rand.Read(b)
            id = hex.EncodeToString(b)
        }
        w.Header().Set("X-Request-ID", id)
        ctx := context.WithValue(r.Context(), requestIDKey, id)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            defer func() {
                if p := recover(); p != nil {
                    logger.ErrorContext(r.Context(), "panic recovered",
                        "panic", p,
                        "stack", string(debug.Stack()))
                    http.Error(w, "internal server error", http.StatusInternalServerError)
                }
            }()
            next.ServeHTTP(w, r)
        })
    }
}
```

---

## 2. HTTP Client — Resilient Configuration

```go
// Production HTTP client — never use http.DefaultClient
func NewHTTPClient(opts ...ClientOption) *http.Client {
    cfg := &clientConfig{
        timeout:             30 * time.Second,
        maxIdleConns:        100,
        maxIdleConnsPerHost: 10,
        idleConnTimeout:     90 * time.Second,
        disableCompression:  false,
    }
    for _, o := range opts { o(cfg) }
    
    transport := &http.Transport{
        DialContext: (&net.Dialer{
            Timeout:   5 * time.Second,
            KeepAlive: 30 * time.Second,
        }).DialContext,
        TLSHandshakeTimeout:   10 * time.Second,
        ResponseHeaderTimeout: cfg.timeout / 2,
        MaxIdleConns:          cfg.maxIdleConns,
        MaxIdleConnsPerHost:   cfg.maxIdleConnsPerHost,
        IdleConnTimeout:       cfg.idleConnTimeout,
        DisableCompression:    cfg.disableCompression,
        ForceAttemptHTTP2:     true,
        TLSClientConfig:       cfg.tlsConfig,
    }
    
    return &http.Client{
        Timeout:   cfg.timeout,
        Transport: transport,
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            if len(via) >= 3 { return fmt.Errorf("too many redirects") }
            return nil
        },
    }
}

// Retry transport with exponential backoff + jitter
type RetryTransport struct {
    Base       http.RoundTripper
    MaxRetries int
    ShouldRetry func(*http.Response, error) bool
}

func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    var (
        resp *http.Response
        err  error
    )
    for attempt := 0; attempt <= t.MaxRetries; attempt++ {
        if attempt > 0 {
            backoff := time.Duration(float64(100*time.Millisecond) *
                math.Pow(2, float64(attempt-1)))
            // Add jitter: ±20%
            jitter := time.Duration(rand.Int63n(int64(backoff) / 5))
            select {
            case <-time.After(backoff + jitter):
            case <-req.Context().Done():
                return nil, req.Context().Err()
            }
            // Clone request for retry (body may be read)
            req = req.Clone(req.Context())
        }
        resp, err = t.Base.RoundTrip(req)
        if !t.ShouldRetry(resp, err) { break }
    }
    return resp, err
}

func DefaultShouldRetry(resp *http.Response, err error) bool {
    if err != nil {
        return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET)
    }
    return resp.StatusCode == http.StatusTooManyRequests ||
        resp.StatusCode >= http.StatusInternalServerError
}
```

---

## 3. gRPC — Production Setup

```go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials"
    "google.golang.org/grpc/keepalive"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    "go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
)

// gRPC server with all production features
func NewGRPCServer(tlsCfg *tls.Config) *grpc.Server {
    return grpc.NewServer(
        grpc.Creds(credentials.NewTLS(tlsCfg)),
        grpc.KeepaliveParams(keepalive.ServerParameters{
            MaxConnectionIdle:     15 * time.Minute,
            MaxConnectionAge:      30 * time.Minute,
            MaxConnectionAgeGrace: 5 * time.Second,
            Time:                  5 * time.Second,
            Timeout:               1 * time.Second,
        }),
        grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
            MinTime:             5 * time.Second,
            PermitWithoutStream: true,
        }),
        grpc.MaxRecvMsgSize(4 << 20), // 4 MB
        grpc.MaxSendMsgSize(4 << 20),
        grpc.ChainUnaryInterceptor(
            grpcRecovery(),
            grpcLogging(),
            otelgrpc.UnaryServerInterceptor(),
        ),
        grpc.ChainStreamInterceptor(
            otelgrpc.StreamServerInterceptor(),
        ),
    )
}

// gRPC client with connection pooling and backoff
func NewGRPCClient(ctx context.Context, addr string, tlsCfg *tls.Config) (*grpc.ClientConn, error) {
    return grpc.DialContext(ctx, addr,
        grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
        grpc.WithKeepaliveParams(keepalive.ClientParameters{
            Time:                10 * time.Second,
            Timeout:             5 * time.Second,
            PermitWithoutStream: true,
        }),
        grpc.WithDefaultCallOptions(
            grpc.WaitForReady(true),
            grpc.MaxCallRecvMsgSize(4<<20),
        ),
        grpc.WithChainUnaryInterceptor(
            otelgrpc.UnaryClientInterceptor(),
        ),
    )
}

// Error mapping: domain errors → gRPC status codes
func ToGRPCStatus(err error) error {
    if err == nil { return nil }
    var ve *ValidationError
    switch {
    case errors.Is(err, ErrNotFound):
        return status.Error(codes.NotFound, err.Error())
    case errors.As(err, &ve):
        return status.Error(codes.InvalidArgument, err.Error())
    case errors.Is(err, context.DeadlineExceeded):
        return status.Error(codes.DeadlineExceeded, err.Error())
    case errors.Is(err, context.Canceled):
        return status.Error(codes.Canceled, err.Error())
    default:
        return status.Error(codes.Internal, "internal server error") // never leak internal details
    }
}
```

---

## 4. TCP Server

```go
// Concurrent TCP server with backpressure
type TCPServer struct {
    listener net.Listener
    sem      chan struct{} // bounded concurrency
    handler  func(context.Context, net.Conn) error
    logger   *slog.Logger
}

func (s *TCPServer) Serve(ctx context.Context) error {
    for {
        conn, err := s.listener.Accept()
        if err != nil {
            if ctx.Err() != nil { return nil } // graceful shutdown
            return fmt.Errorf("accept: %w", err)
        }
        
        select {
        case s.sem <- struct{}{}: // acquire slot
            go func() {
                defer func() { <-s.sem }() // release
                defer conn.Close()
                
                connCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
                defer cancel()
                
                if err := s.handler(connCtx, conn); err != nil {
                    s.logger.WarnContext(connCtx, "conn handler error",
                        "remote", conn.RemoteAddr(), "err", err)
                }
            }()
        case <-ctx.Done():
            conn.Close()
            return nil
        }
    }
}
```

---

## 5. Circuit Breaker

```go
// Circuit breaker (states: closed → open → half-open)
type CircuitBreaker struct {
    mu           sync.Mutex
    state        cbState
    failures     int
    threshold    int
    resetTimeout time.Duration
    lastFailure  time.Time
}

type cbState int
const (cbClosed cbState = iota; cbOpen; cbHalfOpen)

func (cb *CircuitBreaker) Call(fn func() error) error {
    cb.mu.Lock()
    switch cb.state {
    case cbOpen:
        if time.Since(cb.lastFailure) > cb.resetTimeout {
            cb.state = cbHalfOpen
        } else {
            cb.mu.Unlock()
            return fmt.Errorf("circuit open: service unavailable")
        }
    }
    cb.mu.Unlock()
    
    err := fn()
    
    cb.mu.Lock()
    defer cb.mu.Unlock()
    if err != nil {
        cb.failures++
        cb.lastFailure = time.Now()
        if cb.failures >= cb.threshold { cb.state = cbOpen }
    } else {
        cb.failures = 0
        cb.state = cbClosed
    }
    return err
}
```

---

## 6. DNS & Service Discovery

```go
// Custom resolver with caching
type CachingResolver struct {
    cache sync.Map
    ttl   time.Duration
}

type cachedEntry struct {
    addrs   []net.IPAddr
    expires time.Time
}

func (r *CachingResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
    if v, ok := r.cache.Load(host); ok {
        entry := v.(cachedEntry)
        if time.Now().Before(entry.expires) { return entry.addrs, nil }
    }
    addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
    if err != nil { return nil, err }
    r.cache.Store(host, cachedEntry{addrs: addrs, expires: time.Now().Add(r.ttl)})
    return addrs, nil
}
```

---

## Networking Checklist

- [ ] Never use `http.DefaultClient` — always configure timeouts
- [ ] TLS minimum version 1.3 (or 1.2 with strong ciphers only)
- [ ] All connections have read/write/idle timeouts
- [ ] HTTP server has `ReadHeaderTimeout` set (mitigates Slowloris)
- [ ] gRPC keepalive configured for both client and server
- [ ] Retry with exponential backoff + jitter, not fixed delay
- [ ] Circuit breaker wraps all downstream service calls
- [ ] Connection pools sized appropriately (not unbounded)
- [ ] All listener sockets use `SO_REUSEPORT` for multi-process
- [ ] Graceful shutdown drains in-flight requests before closing
