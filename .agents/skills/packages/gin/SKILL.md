---
name: pkg-gin
description: >
  Exhaustive reference for github.com/gin-gonic/gin: router setup, route groups, middleware,
  request binding, validation integration, response helpers, custom error handling, testing,
  and production hardening. Use when gin is the project's HTTP framework. Cross-references:
  networking/SKILL.md, api-design/SKILL.md, packages/validator/SKILL.md.
---

# Package: gin-gonic/gin — Complete Reference

## Import
```go
import "github.com/gin-gonic/gin"
```

## 1. Router Setup (Production)

```go
func NewRouter(h *Handlers, mw *Middlewares) *gin.Engine {
    // Never use gin.Default() in production — it includes logger+recovery
    // with defaults that don't integrate with slog/OTel
    gin.SetMode(gin.ReleaseMode) // always in production
    r := gin.New()

    // Global middleware — order matters
    r.Use(mw.RequestID)
    r.Use(mw.Logger)     // structured slog middleware
    r.Use(mw.Recovery)   // panic recovery → 500
    r.Use(mw.Timeout(30*time.Second))
    r.Use(mw.SecurityHeaders)

    // Health (no auth)
    r.GET("/healthz", h.Health.Liveness)
    r.GET("/readyz",  h.Health.Readiness)

    // API v1
    v1 := r.Group("/v1", mw.Auth, mw.RateLimit)
    {
        orders := v1.Group("/orders")
        {
            orders.GET("",          h.Order.List)
            orders.POST("",         h.Order.Create)
            orders.GET("/:id",      h.Order.Get)
            orders.PATCH("/:id",    h.Order.Update)
            orders.DELETE("/:id",   h.Order.Delete)
            orders.POST("/:id/cancel", h.Order.Cancel)
        }

        users := v1.Group("/users")
        {
            users.GET("",       h.User.List)
            users.POST("",      h.User.Create)
            users.GET("/:id",   h.User.Get)
        }
    }

    // 404 / 405 handlers
    r.NoRoute(func(c *gin.Context) {
        c.JSON(http.StatusNotFound, ErrorResponse{
            Error: ErrorDetail{Code: "NOT_FOUND", Message: "endpoint not found"},
        })
    })
    r.NoMethod(func(c *gin.Context) {
        c.JSON(http.StatusMethodNotAllowed, ErrorResponse{
            Error: ErrorDetail{Code: "METHOD_NOT_ALLOWED", Message: "method not allowed"},
        })
    })
    return r
}
```

## 2. Request Binding & Validation

```go
// ShouldBind validates content type and decodes body
// BindJSON panics on error — use ShouldBindJSON instead
func (h *OrderHandler) Create(c *gin.Context) {
    var req CreateOrderRequest

    // ShouldBindJSON: decodes JSON, returns error (no abort)
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusUnprocessableEntity, ErrorResponse{
            Error: ErrorDetail{Code: "INVALID_REQUEST", Message: err.Error()},
        })
        return
    }

    // Explicit validation with go-playground/validator
    if err := validate.StructCtx(c.Request.Context(), req); err != nil {
        c.JSON(http.StatusUnprocessableEntity, ErrorResponse{
            Error: ErrorDetail{
                Code:    "VALIDATION_ERROR",
                Message: "validation failed",
                Fields:  extractValidationErrors(err),
            },
        })
        return
    }

    order, err := h.svc.CreateOrder(c.Request.Context(), req.toCommand())
    if err != nil {
        h.respondError(c, err)
        return
    }

    c.JSON(http.StatusCreated, Response[OrderView]{Data: toOrderView(order)})
}

// Bind from URI params + query + body simultaneously
type GetOrdersParams struct {
    // URI params
    UserID string `uri:"user_id" validate:"required,uuid4"`
}
type ListOrdersQuery struct {
    Status string `form:"status" validate:"omitempty,oneof=pending confirmed"`
    Limit  int    `form:"limit"  validate:"omitempty,min=1,max=100"`
    Cursor string `form:"cursor"`
}

func (h *OrderHandler) ListByUser(c *gin.Context) {
    var uriParams GetOrdersParams
    if err := c.ShouldBindUri(&uriParams); err != nil {
        c.JSON(http.StatusBadRequest, errorResp("INVALID_URI", err.Error())); return
    }

    var query ListOrdersQuery
    query.Limit = 20 // default
    if err := c.ShouldBindQuery(&query); err != nil {
        c.JSON(http.StatusBadRequest, errorResp("INVALID_QUERY", err.Error())); return
    }
    // ...
}
```

## 3. Path Parameters

```go
// Access path params
id := c.Param("id")     // for routes defined with /:id
// c.Param always returns non-empty string if route matched

// Validate immediately
if _, err := uuid.Parse(id); err != nil {
    c.JSON(http.StatusBadRequest, errorResp("INVALID_ID", "id must be a UUID"))
    return
}
```

## 4. Middleware

```go
// Middleware returns gin.HandlerFunc
func AuthMiddleware(validator TokenValidator) gin.HandlerFunc {
    return func(c *gin.Context) {
        authHeader := c.GetHeader("Authorization")
        if !strings.HasPrefix(authHeader, "Bearer ") {
            c.AbortWithStatusJSON(http.StatusUnauthorized, errorResp("UNAUTHORIZED", "missing token"))
            return
        }
        token := strings.TrimPrefix(authHeader, "Bearer ")

        claims, err := validator.Validate(c.Request.Context(), token)
        if err != nil {
            c.AbortWithStatusJSON(http.StatusUnauthorized, errorResp("UNAUTHORIZED", "invalid token"))
            return
        }

        c.Set("claims", claims)  // store in context
        c.Next()                 // call next handler
    }
}

// Retrieve from context (type-safe helper)
func GetClaims(c *gin.Context) (*Claims, bool) {
    v, exists := c.Get("claims")
    if !exists { return nil, false }
    claims, ok := v.(*Claims)
    return claims, ok
}

// Timeout middleware
func TimeoutMiddleware(timeout time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
        defer cancel()
        c.Request = c.Request.WithContext(ctx)
        c.Next()
    }
}

// Panic recovery with slog
func RecoveryMiddleware(log *slog.Logger) gin.HandlerFunc {
    return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, err any) {
        log.ErrorContext(c.Request.Context(), "panic recovered",
            slog.Any("panic", err),
            slog.String("stack", string(debug.Stack())),
        )
        c.AbortWithStatusJSON(http.StatusInternalServerError,
            errorResp("INTERNAL_ERROR", "internal server error"))
    })
}
```

## 5. Error Handling

```go
// Centralized domain error → HTTP status mapping
func (h *BaseHandler) respondError(c *gin.Context, err error) {
    status := httpStatusFor(err)
    if status >= 500 {
        slog.ErrorContext(c.Request.Context(), "internal error",
            slog.String("path", c.Request.URL.Path),
            slog.Any("err", err))
        c.JSON(status, errorResp("INTERNAL_ERROR", "internal server error"))
        return
    }
    c.JSON(status, errorResp(errorCode(err), userMessage(err)))
}
```

## 6. Testing Gin Handlers

```go
func TestOrderHandler_Create(t *testing.T) {
    t.Parallel()

    gin.SetMode(gin.TestMode)

    svc := mocks.NewMockOrderService(t)
    svc.EXPECT().CreateOrder(mock.Anything, mock.Anything).
        Return(&OrderResponse{ID: "order-1"}, nil).Once()

    r := gin.New()
    r.POST("/orders", NewOrderHandler(svc).Create)

    body := `{"customer_id":"550e8400-e29b-41d4-a716-446655440000","items":[{"product_id":"550e8400-e29b-41d4-a716-446655440001","quantity":2}]}`
    w := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")

    r.ServeHTTP(w, req)

    assert.Equal(t, http.StatusCreated, w.Code)
    var resp Response[OrderView]
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
    assert.Equal(t, "order-1", resp.Data.ID)
}
```

## gin Checklist
- [ ] `gin.SetMode(gin.ReleaseMode)` in production
- [ ] `gin.New()` not `gin.Default()` — custom middleware only
- [ ] `ShouldBindJSON` not `BindJSON` (BindJSON writes 400 and panics internals)
- [ ] `c.AbortWithStatusJSON` in middleware — not `c.JSON` + `return`
- [ ] `c.Next()` called in middleware unless aborting
- [ ] Path params validated (UUID format, etc.) immediately after extraction
- [ ] `gin.SetMode(gin.TestMode)` in all test files
- [ ] `NoRoute` and `NoMethod` handlers set for proper 404/405 responses
- [ ] Context from `c.Request.Context()` passed to all service calls
- [ ] `c.Set` / `c.Get` for context values — typed wrapper functions
