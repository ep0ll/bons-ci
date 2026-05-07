---
name: golang-api-design
description: >
  Go API design: RESTful resource design, versioning, request/response contracts, OpenAPI specs,
  gRPC service design with proto3, pagination (cursor and offset), filtering, sorting, partial
  updates (PATCH), bulk operations, idempotency keys, rate limiting headers, ETag/conditional
  requests, HATEOAS links, and API evolution without breaking changes. Use for any HTTP or gRPC
  API surface design. Always combine with networking/SKILL.md and security/SKILL.md.
---

# Go API Design — REST & gRPC Production Patterns

## 1. REST Resource Naming

```
# Resource naming: nouns, plural, kebab-case
GET    /v1/users                    # list
POST   /v1/users                    # create
GET    /v1/users/{id}               # get one
PUT    /v1/users/{id}               # replace (full update)
PATCH  /v1/users/{id}               # partial update
DELETE /v1/users/{id}               # delete

# Nested resources (max 2 levels deep)
GET    /v1/users/{userId}/orders
POST   /v1/users/{userId}/orders
GET    /v1/users/{userId}/orders/{orderId}

# Actions (when CRUD doesn't fit — use POST + verb noun)
POST   /v1/users/{id}/activate
POST   /v1/orders/{id}/cancel
POST   /v1/payments/{id}/refund

# Search / filter as query params
GET    /v1/orders?status=pending&created_after=2024-01-01&sort=created_at:desc&limit=20&cursor=xxx

# Version in path (not header — clients must opt in to upgrades)
/v1/... /v2/...
```

---

## 2. Request / Response Envelope

```go
// Standard response envelope — consistent across all endpoints
type Response[T any] struct {
    Data    T          `json:"data"`
    Meta    *PageMeta  `json:"meta,omitempty"`
    Links   *Links     `json:"links,omitempty"`
}

type PageMeta struct {
    Total      int64  `json:"total"`
    Limit      int    `json:"limit"`
    NextCursor string `json:"next_cursor,omitempty"`
    HasMore    bool   `json:"has_more"`
}

type Links struct {
    Self  string `json:"self"`
    Next  string `json:"next,omitempty"`
    Prev  string `json:"prev,omitempty"`
}

// Error response — consistent, never leaks internals
type ErrorResponse struct {
    Error ErrorDetail `json:"error"`
}
type ErrorDetail struct {
    Code    string         `json:"code"`       // machine-readable: "VALIDATION_ERROR"
    Message string         `json:"message"`    // human-readable
    Fields  map[string]string `json:"fields,omitempty"` // for validation errors
    TraceID string         `json:"trace_id,omitempty"`  // for debugging
}
```

---

## 3. Request Parsing & Validation

```go
// Handler pattern: decode → validate → authorize → handle → respond
func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
    // 1. Decode (size-limited, strict)
    r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB max
    var req CreateOrderRequest
    dec := json.NewDecoder(r.Body)
    dec.DisallowUnknownFields() // reject extra fields
    if err := dec.Decode(&req); err != nil {
        h.respondError(w, r, ErrValidation, "invalid JSON: "+sanitizeDecodeErr(err))
        return
    }

    // 2. Validate
    if err := h.validator.StructCtx(r.Context(), req); err != nil {
        h.respondValidationError(w, r, err)
        return
    }

    // 3. Handle
    order, err := h.svc.CreateOrder(r.Context(), req.toCommand())
    if err != nil {
        h.respondError(w, r, err, "")
        return
    }

    // 4. Respond
    h.respondJSON(w, http.StatusCreated, Response[OrderView]{Data: toOrderView(order)})
}

// CreateOrderRequest — all fields must be validated
type CreateOrderRequest struct {
    CustomerID string      `json:"customer_id" validate:"required,uuid4"`
    Items      []ItemInput `json:"items"       validate:"required,min=1,max=100,dive"`
}

type ItemInput struct {
    ProductID string `json:"product_id" validate:"required,uuid4"`
    Quantity  int    `json:"quantity"   validate:"required,min=1,max=1000"`
}
```

---

## 4. Cursor-Based Pagination

```go
// Cursor pagination — stable across concurrent inserts/deletes
// Cursor = base64-encoded (last_id, last_created_at) tuple

type CursorPage[T any] struct {
    Items      []T    `json:"items"`
    NextCursor string `json:"next_cursor,omitempty"`
    HasMore    bool   `json:"has_more"`
}

type Cursor struct {
    ID        string    `json:"id"`
    CreatedAt time.Time `json:"created_at"`
}

func EncodeCursor(id string, t time.Time) string {
    data, _ := json.Marshal(Cursor{ID: id, CreatedAt: t})
    return base64.URLEncoding.EncodeToString(data)
}

func DecodeCursor(s string) (Cursor, error) {
    data, err := base64.URLEncoding.DecodeString(s)
    if err != nil { return Cursor{}, fmt.Errorf("invalid cursor: %w", err) }
    var c Cursor
    if err := json.Unmarshal(data, &c); err != nil {
        return Cursor{}, fmt.Errorf("invalid cursor content: %w", err)
    }
    return c, nil
}

// SQL: keyset pagination (efficient — uses index)
// WHERE (created_at, id) < ($cursor_time, $cursor_id)
// ORDER BY created_at DESC, id DESC
// LIMIT $limit + 1  -- fetch one extra to determine HasMore
func buildCursorQuery(cursor *Cursor, limit int) (string, []any) {
    args := []any{limit + 1}
    q := "SELECT * FROM orders"
    if cursor != nil {
        q += " WHERE (created_at, id) < ($2, $3)"
        args = append(args, cursor.CreatedAt, cursor.ID)
    }
    q += " ORDER BY created_at DESC, id DESC LIMIT $1"
    return q, args
}
```

---

## 5. PATCH (Partial Update)

```go
// Use JSON Merge Patch (RFC 7396) or explicit optional fields
// JSON Merge Patch: send only fields to change; null = delete field

// Option A: pointer fields (null = "set to null/zero", missing = "don't touch")
type UpdateUserRequest struct {
    Name  *string `json:"name,omitempty"`  // nil = don't change
    Email *string `json:"email,omitempty"` // nil = don't change
    Bio   *string `json:"bio"`             // nil = clear the field
}

func (r *UpdateUserRequest) Apply(u *User) {
    if r.Name != nil  { u.Name = *r.Name }
    if r.Email != nil { u.Email = *r.Email }
    if r.Bio != nil   { u.Bio = *r.Bio }   // including empty string (clear)
}

// Option B: field mask (more explicit, gRPC-friendly)
type UpdateUserRequest struct {
    User      UserInput `json:"user"`
    FieldMask []string  `json:"field_mask"` // ["name","email"]
}
```

---

## 6. Conditional Requests (ETag)

```go
// ETags prevent lost updates and enable cache validation
func (h *Handler) GetOrder(w http.ResponseWriter, r *http.Request) {
    order, err := h.svc.GetOrder(r.Context(), r.PathValue("id"))
    if err != nil { h.respondError(w, r, err, ""); return }

    etag := computeETag(order) // e.g., `"` + hash of (id+version) + `"`

    // Conditional GET: 304 if client has current version
    if r.Header.Get("If-None-Match") == etag {
        w.WriteHeader(http.StatusNotModified)
        return
    }

    w.Header().Set("ETag", etag)
    w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
    h.respondJSON(w, http.StatusOK, order)
}

func (h *Handler) UpdateOrder(w http.ResponseWriter, r *http.Request) {
    // Optimistic concurrency via If-Match
    ifMatch := r.Header.Get("If-Match")
    if ifMatch == "" {
        h.respondError(w, r, ErrValidation, "If-Match header required for updates")
        return
    }
    // Pass version to service for optimistic lock check
    // Service returns ErrConflict → 412 Precondition Failed
}

func computeETag(v any) string {
    data, _ := json.Marshal(v)
    sum := sha256.Sum256(data)
    return `"` + hex.EncodeToString(sum[:8]) + `"`
}
```

---

## 7. API Versioning Strategy

```go
// V1 handler — frozen API contract
// /v1/users returns UserV1 shape — never changes
type UserV1 struct {
    ID        string `json:"id"`
    Name      string `json:"name"`
    Email     string `json:"email"`
    CreatedAt string `json:"created_at"` // string in v1 (mistake)
}

// V2 handler — improved API
// /v2/users returns UserV2 shape
type UserV2 struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    Email     string    `json:"email"`
    CreatedAt time.Time `json:"created_at"` // proper time.Time in v2
    UpdatedAt time.Time `json:"updated_at"` // new field in v2
}

// Router — both versions served simultaneously
func RegisterRoutes(r chi.Router, h *Handlers) {
    r.Route("/v1", func(r chi.Router) {
        r.Get("/users/{id}", h.V1.GetUser)
    })
    r.Route("/v2", func(r chi.Router) {
        r.Get("/users/{id}", h.V2.GetUser)
    })
}

// Deprecation: return header on v1 responses
w.Header().Set("Deprecation", "true")
w.Header().Set("Sunset", "Sat, 01 Jan 2026 00:00:00 GMT")
w.Header().Set("Link", `</v2/users>; rel="successor-version"`)
```

---

## 8. gRPC Service Design

```protobuf
// api/order/v1/order_service.proto
syntax = "proto3";
package order.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/field_mask.proto";

service OrderService {
  rpc CreateOrder(CreateOrderRequest) returns (Order);
  rpc GetOrder(GetOrderRequest) returns (Order);
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse);
  rpc UpdateOrder(UpdateOrderRequest) returns (Order);
  rpc CancelOrder(CancelOrderRequest) returns (Order);
}

message CreateOrderRequest {
  string customer_id = 1;        // required
  repeated LineItem items = 2;   // required, min=1
  string idempotency_key = 3;    // required for safe retries
}

message ListOrdersRequest {
  string customer_id = 1;
  int32 page_size = 2;           // max 100, default 20
  string page_token = 3;         // cursor
  string filter = 4;             // "status=pending"
  string order_by = 5;           // "created_at desc"
}

message ListOrdersResponse {
  repeated Order orders = 1;
  string next_page_token = 2;
  int32 total_size = 3;
}

// Field mask for partial updates (AIP-134)
message UpdateOrderRequest {
  Order order = 1;
  google.protobuf.FieldMask update_mask = 2;
}
```

---

## API Design Checklist

- [ ] All endpoints versioned (`/v1/`, `/v2/`)
- [ ] Consistent envelope: `{data: ..., meta: ..., links: ...}`
- [ ] Consistent error: `{error: {code, message, fields, trace_id}}`
- [ ] Cursor-based pagination for all list endpoints
- [ ] PATCH uses pointer fields or field mask — never full-replace via PATCH
- [ ] ETag + If-Match for all mutable resources
- [ ] Idempotency-Key required on POST endpoints that create resources
- [ ] OpenAPI/proto spec committed alongside code
- [ ] Deprecated endpoints return `Deprecation` + `Sunset` headers
- [ ] `Content-Type: application/json` validated on all POST/PUT/PATCH
- [ ] Request body size limited with `http.MaxBytesReader`
- [ ] `DisallowUnknownFields()` on JSON decoder for strict validation
