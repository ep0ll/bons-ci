---
name: pkg-chi
description: >
  Exhaustive reference for github.com/go-chi/chi/v5: router setup, route groups, middleware
  stack, URL parameters, sub-routers, chi.URLParam, chi.RouteContext, built-in middleware,
  custom middleware patterns, testing chi handlers, and chi + OpenAPI integration.
  Primary HTTP router for all services. Cross-references: networking/SKILL.md, api-design/SKILL.md.
---

# Package: go-chi/chi/v5 — Complete Reference

## Import
```go
import (
    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"
)
```

## 1. Router Setup

```go
func NewRouter(h *Handlers, mw *Middlewares) http.Handler {
    r := chi.NewRouter()

    // ── GLOBAL MIDDLEWARE (order matters — executes top to bottom) ──
    r.Use(middleware.RequestID)           // inject X-Request-ID
    r.Use(middleware.RealIP)             // use X-Forwarded-For
    r.Use(mw.Logger)                     // structured request logging
    r.Use(mw.Recover)                    // panic recovery → 500
    r.Use(middleware.CleanPath)          // normalize URL paths
    r.Use(middleware.StripSlashes)       // /users/ → /users
    r.Use(middleware.Timeout(30*time.Second)) // request timeout
    r.Use(mw.SecurityHeaders)            // HSTS, CSP, X-Frame, etc.
    r.Use(mw.CORS)                       // CORS headers

    // ── HEALTH (no auth, no rate limit) ────────────────────────────
    r.Get("/healthz", h.Health.Liveness)
    r.Get("/readyz",  h.Health.Readiness)
    r.Handle("/metrics", promhttp.Handler())

    // ── API v1 ──────────────────────────────────────────────────────
    r.Route("/v1", func(r chi.Router) {
        r.Use(mw.Auth)          // auth for all /v1 routes
        r.Use(mw.RateLimit)     // rate limiting

        r.Route("/users", func(r chi.Router) {
            r.Get("/",          h.User.List)
            r.Post("/",         h.User.Create)
            r.Route("/{userID}", func(r chi.Router) {
                r.Use(mw.UserCtx)  // load user into context
                r.Get("/",         h.User.Get)
                r.Put("/",         h.User.Replace)
                r.Patch("/",       h.User.Update)
                r.Delete("/",      h.User.Delete)
                r.Post("/activate", h.User.Activate)
            })
        })

        r.Route("/orders", func(r chi.Router) {
            r.Get("/",    h.Order.List)
            r.Post("/",   h.Order.Create)
            r.Get("/{id}", h.Order.Get)
        })
    })

    // ── API v2 ──────────────────────────────────────────────────────
    r.Mount("/v2", v2Router(h, mw))

    return r
}
```

## 2. URL Parameters

```go
// Get URL param — use chi.URLParam (never r.PathValue in chi)
func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
    userID := chi.URLParam(r, "userID")
    if userID == "" {
        h.respondError(w, r, ErrValidation, "userID required")
        return
    }
    // validate UUID format
    if _, err := uuid.Parse(userID); err != nil {
        h.respondError(w, r, ErrValidation, "userID must be a valid UUID")
        return
    }
    // ...
}

// Multiple URL params
func (h *Handler) GetOrderItem(w http.ResponseWriter, r *http.Request) {
    orderID := chi.URLParam(r, "orderID")
    itemID  := chi.URLParam(r, "itemID")
    // ...
}
```

## 3. Context Middleware Pattern

```go
// Load resource into context in middleware — handler just reads it
func UserCtx(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        userID := chi.URLParam(r, "userID")
        user, err := userRepo.FindByID(r.Context(), userID)
        if err != nil {
            if errors.Is(err, domain.ErrNotFound) {
                respondError(w, r, http.StatusNotFound, "user not found")
                return
            }
            respondError(w, r, http.StatusInternalServerError, "internal error")
            return
        }
        ctx := context.WithValue(r.Context(), userCtxKey{}, user)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// Handler reads from context — clean, no repeated DB calls
func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
    user := r.Context().Value(userCtxKey{}).(*User)
    h.respondJSON(w, http.StatusOK, toUserView(user))
}
```

## 4. Query Parameter Parsing

```go
// Structured query param parsing — never manual string extraction
type ListOrdersParams struct {
    Status   string    `schema:"status"`
    After    time.Time `schema:"after"`
    Limit    int       `schema:"limit"`
    Cursor   string    `schema:"cursor"`
    Sort     string    `schema:"sort"`
}

// Use gorilla/schema or manual parsing:
func parseListParams(r *http.Request) (ListOrdersParams, error) {
    q := r.URL.Query()
    params := ListOrdersParams{
        Status: q.Get("status"),
        Cursor: q.Get("cursor"),
        Sort:   q.Get("sort"),
    }
    if limitStr := q.Get("limit"); limitStr != "" {
        n, err := strconv.Atoi(limitStr)
        if err != nil || n < 1 || n > 100 {
            return params, fmt.Errorf("limit must be 1-100")
        }
        params.Limit = n
    } else {
        params.Limit = 20  // default
    }
    return params, nil
}
```

## 5. Custom Middleware

```go
// Middleware signature: func(http.Handler) http.Handler
// Always pass context down — don't create Background context in middleware

func AuthMiddleware(tokenValidator TokenValidator) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Extract token
            authHeader := r.Header.Get("Authorization")
            if !strings.HasPrefix(authHeader, "Bearer ") {
                respondError(w, r, http.StatusUnauthorized, "missing bearer token")
                return
            }
            token := strings.TrimPrefix(authHeader, "Bearer ")

            // Validate
            claims, err := tokenValidator.Validate(r.Context(), token)
            if err != nil {
                respondError(w, r, http.StatusUnauthorized, "invalid token")
                return
            }

            // Inject into context
            ctx := context.WithValue(r.Context(), claimsKey{}, claims)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// Response writer wrapper for status code capture in logging
type responseWriter struct {
    http.ResponseWriter
    status  int
    written int64
}

func (rw *responseWriter) WriteHeader(code int) {
    rw.status = code
    rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
    n, err := rw.ResponseWriter.Write(b)
    rw.written += int64(n)
    return n, err
}
```

## 6. Sub-Router Mounting

```go
// Mount: prefix another router or handler at a path
r.Mount("/admin", adminRouter())
r.Mount("/v2", v2Router())

// FileServer for static assets
workDir, _ := os.Getwd()
filesDir := http.Dir(filepath.Join(workDir, "static"))
r.Handle("/static/*", http.StripPrefix("/static", http.FileServer(filesDir)))
```

## 7. Testing chi Handlers

```go
func TestUserHandler_Create(t *testing.T) {
    t.Parallel()

    // Create router with all real middleware (integration-style)
    r := chi.NewRouter()
    r.Post("/users", handler.Create)

    body := `{"name":"Alice","email":"alice@example.com"}`
    req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Request-ID", "test-123")

    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    assert.Equal(t, http.StatusCreated, w.Code)
    var resp Response[UserView]
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
    assert.Equal(t, "Alice", resp.Data.Name)
}

// Test with URL params (chi requires route to be registered)
func TestUserHandler_Get(t *testing.T) {
    r := chi.NewRouter()
    r.Get("/users/{userID}", handler.Get)

    req := httptest.NewRequest(http.MethodGet, "/users/"+testUserID, nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)  // chi resolves URL params through registered route

    // chi.URLParamFromCtx also works in tests after serving through the router
}
```

## 8. Built-in Middleware Reference

```go
middleware.RequestID         // injects X-Request-ID (generates if absent)
middleware.RealIP            // sets r.RemoteAddr from X-Forwarded-For / X-Real-IP
middleware.Logger            // basic stdlib logger (replace with slog version)
middleware.Recoverer         // recover from panics → 500
middleware.Compress(5)       // gzip response compression (level 5)
middleware.CleanPath         // clean double slashes
middleware.StripSlashes      // remove trailing slashes
middleware.RedirectSlashes   // redirect trailing slashes instead of strip
middleware.Timeout(d)        // per-request context deadline
middleware.Heartbeat("/ping") // lightweight healthcheck endpoint
middleware.NoCache           // set no-cache headers
middleware.SetHeader(k, v)   // set response header
middleware.BasicAuth("realm", map[string]string{"user":"pass"})
middleware.Throttle(limit)   // token-bucket throttling (requests/s)
middleware.ThrottleBacklog(limit, backlog, timeout)
```

## chi Checklist
- [ ] `chi.URLParam(r, "name")` for path parameters — not `r.PathValue`
- [ ] Route groups (`r.Route`) for versioning and shared middleware
- [ ] Context middleware loads resources once — handlers read from context
- [ ] Response writer wrapped to capture status code in logging middleware
- [ ] `middleware.Timeout` set on all routes (not just server-level write timeout)
- [ ] Tests use `r.ServeHTTP` via registered router — not handler directly
- [ ] `middleware.RequestID` first in middleware chain
- [ ] `middleware.Recoverer` before any business middleware
