---
name: pkg-http-std
description: >
  Exhaustive reference for Go's net/http stdlib: http.Server configuration, http.Client
  hardening, Handler and HandlerFunc, ServeMux (Go 1.22+), middleware patterns, request
  parsing, response writing, file serving, reverse proxying, timeouts hierarchy, connection
  management, and testing with httptest. Cross-references: networking/SKILL.md, api-design/SKILL.md.
---

# Package: net/http — Complete Reference

## Import
```go
import (
    "net/http"
    "net/http/httptest"
    "net/http/httputil"
)
```

## 1. Production Server

```go
func NewServer(handler http.Handler, cfg ServerConfig) *http.Server {
    return &http.Server{
        Addr:    cfg.Addr,
        Handler: handler,

        // Timeouts — all must be set; no single default covers everything
        ReadTimeout:       15 * time.Second, // time to read entire request including body
        ReadHeaderTimeout: 5 * time.Second,  // time to read headers only (mitigates Slowloris)
        WriteTimeout:      15 * time.Second, // time to write response
        IdleTimeout:       120 * time.Second,// keep-alive connection idle time
        MaxHeaderBytes:    1 << 20,          // 1 MB max header size

        // Structured error logging — never use default log.Printf
        ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
    }
}

// Graceful shutdown
func RunServer(ctx context.Context, srv *http.Server) error {
    errCh := make(chan error, 1)
    go func() {
        if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil &&
            !errors.Is(err, http.ErrServerClosed) {
            errCh <- err
        }
    }()

    select {
    case <-ctx.Done():
        shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        return srv.Shutdown(shutCtx)
    case err := <-errCh:
        return fmt.Errorf("server: %w", err)
    }
}
```

## 2. Production HTTP Client

```go
// NEVER use http.DefaultClient — it has no timeouts
func NewClient(cfg ClientConfig) *http.Client {
    return &http.Client{
        Timeout: cfg.Timeout, // end-to-end timeout including redirects

        Transport: &http.Transport{
            DialContext: (&net.Dialer{
                Timeout:   5 * time.Second,  // TCP connect timeout
                KeepAlive: 30 * time.Second, // TCP keepalive
            }).DialContext,
            TLSHandshakeTimeout:   10 * time.Second,
            ResponseHeaderTimeout: cfg.Timeout / 2, // waiting for first response byte
            ExpectContinueTimeout: 1 * time.Second,
            MaxIdleConns:          100,
            MaxIdleConnsPerHost:   10,
            IdleConnTimeout:       90 * time.Second,
            DisableCompression:    false, // keep gzip enabled
            ForceAttemptHTTP2:     true,
            TLSClientConfig:       cfg.TLS,
        },

        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            if len(via) >= 3 { return fmt.Errorf("too many redirects") }
            // Don't forward Authorization header on redirect to different host
            if req.URL.Host != via[0].URL.Host {
                req.Header.Del("Authorization")
            }
            return nil
        },
    }
}
```

## 3. Go 1.22 ServeMux (Enhanced Routing)

```go
// Go 1.22+: method+path pattern matching, path params
mux := http.NewServeMux()

// Method routing
mux.HandleFunc("GET /users", listUsers)
mux.HandleFunc("POST /users", createUser)
mux.HandleFunc("GET /users/{id}", getUser)        // {id} = wildcard
mux.HandleFunc("PUT /users/{id}", updateUser)
mux.HandleFunc("DELETE /users/{id}", deleteUser)
mux.HandleFunc("GET /files/{path...}", serveFile) // {path...} = remainder wildcard

// Extract path params
func getUser(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id") // Go 1.22+
    if id == "" {
        http.Error(w, "missing id", http.StatusBadRequest)
        return
    }
}
```

## 4. Handler Pattern

```go
// HandlerFunc for simple handlers
func healthHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _, _ = io.WriteString(w, `{"status":"ok"}`)
}

// Handler interface for stateful handlers
type UserHandler struct {
    svc    UserService
    logger *slog.Logger
}

func (h *UserHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Dispatch by method
    switch r.Method {
    case http.MethodGet:    h.get(w, r)
    case http.MethodPost:   h.create(w, r)
    default:
        w.Header().Set("Allow", "GET, POST")
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
    }
}

// Response helpers — always set Content-Type before WriteHeader
func respondJSON(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.Header().Set("X-Content-Type-Options", "nosniff")
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(v); err != nil {
        slog.Error("respondJSON encode", "err", err)
    }
}

// IMPORTANT: WriteHeader must be called AFTER setting headers, BEFORE writing body
// Calling Write() without WriteHeader first implicitly calls WriteHeader(200)
// Once WriteHeader is called, headers are frozen
```

## 5. Request Parsing

```go
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
    // 1. Limit body size — ALWAYS
    r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB

    // 2. Validate Content-Type
    ct := r.Header.Get("Content-Type")
    if !strings.HasPrefix(ct, "application/json") {
        http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
        return
    }

    // 3. Decode strictly
    var req CreateRequest
    dec := json.NewDecoder(r.Body)
    dec.DisallowUnknownFields()
    if err := dec.Decode(&req); err != nil {
        var maxErr *http.MaxBytesError
        if errors.As(err, &maxErr) {
            http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
            return
        }
        http.Error(w, "invalid JSON: "+sanitize(err), http.StatusUnprocessableEntity)
        return
    }

    // 4. Check no extra data after JSON object
    if dec.More() {
        http.Error(w, "request body must contain only one JSON object", http.StatusUnprocessableEntity)
        return
    }
}
```

## 6. Middleware

```go
// Middleware signature: func(http.Handler) http.Handler
// Chain function for composing middleware
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
    for i := len(mw) - 1; i >= 0; i-- { h = mw[i](h) }
    return h
}

// Request ID middleware
func RequestID(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := r.Header.Get("X-Request-ID")
        if id == "" {
            b := make([]byte, 16)
            _, _ = rand.Read(b)
            id = hex.EncodeToString(b)
        }
        w.Header().Set("X-Request-ID", id)
        next.ServeHTTP(w, r.WithContext(
            context.WithValue(r.Context(), requestIDKey{}, id),
        ))
    })
}

// Wrapped ResponseWriter to capture status
type statusWriter struct {
    http.ResponseWriter
    status  int
    written int64
}
func (sw *statusWriter) WriteHeader(code int) {
    sw.status = code
    sw.ResponseWriter.WriteHeader(code)
}
func (sw *statusWriter) Write(b []byte) (int, error) {
    n, err := sw.ResponseWriter.Write(b)
    sw.written += int64(n)
    return n, err
}
// Implement http.Flusher, http.Hijacker etc. via delegation if needed
```

## 7. Reverse Proxy

```go
func NewReverseProxy(target *url.URL, timeout time.Duration) http.Handler {
    proxy := httputil.NewSingleHostReverseProxy(target)
    proxy.Transport = &http.Transport{
        DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
        TLSHandshakeTimeout: 5 * time.Second,
        ResponseHeaderTimeout: timeout,
    }
    proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
        slog.ErrorContext(r.Context(), "proxy error", "target", target, "err", err)
        http.Error(w, "bad gateway", http.StatusBadGateway)
    }
    proxy.ModifyResponse = func(resp *http.Response) error {
        // Remove internal headers before forwarding to client
        resp.Header.Del("X-Internal-Server")
        return nil
    }
    return proxy
}
```

## 8. Testing

```go
func TestHandler_Get(t *testing.T) {
    t.Parallel()

    // httptest.NewRecorder captures response
    w := httptest.NewRecorder()
    r := httptest.NewRequest(http.MethodGet, "/users/123", nil)
    r.Header.Set("Authorization", "Bearer test-token")

    handler := &UserHandler{svc: mockSvc}
    handler.ServeHTTP(w, r)

    resp := w.Result()
    defer resp.Body.Close()

    assert.Equal(t, http.StatusOK, resp.StatusCode)
    assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

    var body UserResponse
    require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
    assert.Equal(t, "123", body.ID)
}

// httptest.NewServer for full network round-trip tests
func TestServer_Integration(t *testing.T) {
    ts := httptest.NewTLSServer(handler)
    defer ts.Close()

    client := ts.Client() // pre-configured to trust test TLS cert
    resp, err := client.Get(ts.URL + "/users")
    require.NoError(t, err)
    defer resp.Body.Close()
    assert.Equal(t, http.StatusOK, resp.StatusCode)
}
```

## net/http Checklist
- [ ] Server has ALL timeout fields set: `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`
- [ ] `http.DefaultClient` never used — always custom client with timeouts
- [ ] `http.MaxBytesReader` applied to all request bodies
- [ ] `dec.DisallowUnknownFields()` on all JSON decoders
- [ ] `Content-Type` validated before parsing body
- [ ] Headers set BEFORE `WriteHeader()` call
- [ ] `WriteHeader()` called exactly once per response
- [ ] Custom `ErrorLog` on server — never default `log.Printf`
- [ ] `srv.Shutdown()` used for graceful stop — never `srv.Close()`
- [ ] `httptest.NewRecorder` + `httptest.NewRequest` for unit tests
