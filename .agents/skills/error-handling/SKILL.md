---
name: golang-error-handling
description: >
  Exhaustive Go error handling: error taxonomy (sentinel, typed, wrapped), error propagation
  strategy, custom error types with context, errors.Is/As unwrapping, multi-error aggregation,
  error recovery patterns, panic vs error policy, HTTP/gRPC error mapping, and error logging
  hygiene. Use for any error design decision. Non-negotiable on every file.
---

# Go Error Handling — Complete Strategy

## 1. Error Taxonomy

### Tier 1: Sentinel Errors (for fixed, identity-comparable conditions)
```go
// Define in the package that OWNS the concept
var (
    ErrNotFound   = errors.New("not found")
    ErrConflict   = errors.New("conflict")
    ErrUnauthorized = errors.New("unauthorized")
)

// Check with errors.Is — works through wrapping chains
if errors.Is(err, ErrNotFound) { /* handle */ }
```

### Tier 2: Typed Errors (for errors carrying structured context)
```go
type ValidationError struct {
    Field   string
    Value   any
    Message string
}
func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation: field=%q value=%v: %s", e.Field, e.Value, e.Message)
}

type NotFoundError struct {
    Resource string
    ID       any
}
func (e *NotFoundError) Error() string {
    return fmt.Sprintf("%s with id=%v not found", e.Resource, e.ID)
}
func (e *NotFoundError) Is(target error) bool {
    _, ok := target.(*NotFoundError); return ok
}

// Check with errors.As
var ve *ValidationError
if errors.As(err, &ve) {
    fmt.Printf("field %s failed: %s\n", ve.Field, ve.Message)
}
```

### Tier 3: Wrapped Errors (always add context on propagation)
```go
// RULE: wrap at every boundary crossing — caller gets full context chain
func (s *UserService) Register(ctx context.Context, req RegisterRequest) (*User, error) {
    user, err := s.repo.Save(ctx, &User{Email: req.Email})
    if err != nil {
        // Format: ReceiverType.MethodName(key_param=value): cause
        return nil, fmt.Errorf("UserService.Register(email=%q): %w", req.Email, err)
    }
    return user, nil
}
// Error chain: "UserService.Register(email="x"): PostgresRepo.Save: pgx: duplicate key value"
```

---

## 2. Multi-Error Aggregation

```go
import "errors"

// errors.Join (Go 1.20+) — aggregate multiple errors
func ValidateAll(req Request) error {
    var errs []error
    if req.Name == "" {
        errs = append(errs, &ValidationError{Field: "name", Message: "required"})
    }
    if req.Email == "" {
        errs = append(errs, &ValidationError{Field: "email", Message: "required"})
    }
    if len(req.Email) > 254 {
        errs = append(errs, &ValidationError{Field: "email", Message: "too long"})
    }
    return errors.Join(errs...) // nil if errs is empty
}

// Custom multi-error for richer output
type MultiError struct {
    Errors []error
}
func (m *MultiError) Error() string {
    msgs := make([]string, len(m.Errors))
    for i, e := range m.Errors { msgs[i] = e.Error() }
    return fmt.Sprintf("%d errors: [%s]", len(m.Errors), strings.Join(msgs, "; "))
}
func (m *MultiError) Unwrap() []error { return m.Errors } // Go 1.20+
```

---

## 3. Propagation Rules

```go
// RULE 1: Handle or propagate — never silently swallow
// ✗ BAD
user, _ := repo.FindByID(ctx, id)
// ✓ GOOD
user, err := repo.FindByID(ctx, id)
if err != nil { return nil, fmt.Errorf("Handler.GetUser: %w", err) }

// RULE 2: Wrap once per boundary — not at every internal call
// ✗ BAD — wrapping inside the repo implementation
func (r *Repo) FindByID(ctx context.Context, id string) (*User, error) {
    err := r.db.QueryRow(...)
    return nil, fmt.Errorf("Repo.FindByID: %w", err) // wrap here...
}
func (s *Service) GetUser(...) (*User, error) {
    u, err := r.repo.FindByID(ctx, id)
    return nil, fmt.Errorf("Service.GetUser: %w", err) // ...and wrap again = double wrap ✓ (this IS ok)
}
// Actually: wrapping at each layer IS correct — builds a call trace

// RULE 3: Enrich at the caller, not the callee
// Callee returns minimal error; caller adds context it has
return nil, fmt.Errorf("UserService.Register(email=%q, role=%s): %w", email, role, err)

// RULE 4: Don't wrap sentinel errors that ARE the answer
// ✗ BAD
return nil, fmt.Errorf("not found: %w", ErrNotFound) // redundant
// ✓ GOOD
return nil, fmt.Errorf("UserRepo.FindByID(id=%s): %w", id, ErrNotFound)
```

---

## 4. Domain Error Package Pattern

```go
// internal/domain/errors.go — one place for all domain errors
package domain

import "errors"

// Sentinel errors for identity checks
var (
    ErrNotFound     = errors.New("not found")
    ErrConflict     = errors.New("conflict: version mismatch")
    ErrUnauthorized = errors.New("unauthorized")
    ErrForbidden    = errors.New("forbidden")
    ErrValidation   = errors.New("validation failed")
    ErrRateLimit    = errors.New("rate limit exceeded")
)

// Is checks satisfy sentinel semantics for typed errors
type NotFoundError struct{ Resource, ID string }
func (e *NotFoundError) Error() string { return fmt.Sprintf("%s %q not found", e.Resource, e.ID) }
func (e *NotFoundError) Is(t error) bool { return t == ErrNotFound }
// errors.Is(err, domain.ErrNotFound) works for both *NotFoundError and ErrNotFound

type ValidationError struct {
    Fields map[string]string // field → message
}
func (e *ValidationError) Error() string {
    pairs := make([]string, 0, len(e.Fields))
    for f, m := range e.Fields { pairs = append(pairs, f+": "+m) }
    sort.Strings(pairs)
    return "validation: " + strings.Join(pairs, ", ")
}
func (e *ValidationError) Is(t error) bool { return t == ErrValidation }
```

---

## 5. HTTP Error Mapping

```go
// Translate domain errors to HTTP status codes at the adapter boundary
func httpStatusFor(err error) int {
    switch {
    case errors.Is(err, domain.ErrNotFound):     return http.StatusNotFound
    case errors.Is(err, domain.ErrConflict):     return http.StatusConflict
    case errors.Is(err, domain.ErrUnauthorized): return http.StatusUnauthorized
    case errors.Is(err, domain.ErrForbidden):    return http.StatusForbidden
    case errors.Is(err, domain.ErrValidation):   return http.StatusUnprocessableEntity
    case errors.Is(err, domain.ErrRateLimit):    return http.StatusTooManyRequests
    case errors.Is(err, context.DeadlineExceeded): return http.StatusGatewayTimeout
    case errors.Is(err, context.Canceled):         return http.StatusServiceUnavailable
    default:                                       return http.StatusInternalServerError
    }
}

// Structured error response — never leak internal error details to clients
type APIError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
    Details any    `json:"details,omitempty"`
}

func respondError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
    status := httpStatusFor(err)

    var apiErr APIError
    var ve *domain.ValidationError
    switch {
    case errors.As(err, &ve):
        apiErr = APIError{Code: "VALIDATION_ERROR", Message: "validation failed", Details: ve.Fields}
    case status >= 500:
        // Log full error internally — never expose to client
        log.ErrorContext(r.Context(), "internal error", "err", err,
            "path", r.URL.Path, "method", r.Method)
        apiErr = APIError{Code: "INTERNAL_ERROR", Message: "an internal error occurred"}
    default:
        apiErr = APIError{Code: errorCode(err), Message: userMessage(err)}
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(apiErr)
}
```

---

## 6. gRPC Error Mapping

```go
import "google.golang.org/grpc/status"
import "google.golang.org/grpc/codes"

func toGRPCError(err error) error {
    if err == nil { return nil }
    switch {
    case errors.Is(err, domain.ErrNotFound):     return status.Error(codes.NotFound, sanitize(err))
    case errors.Is(err, domain.ErrConflict):     return status.Error(codes.Aborted, sanitize(err))
    case errors.Is(err, domain.ErrUnauthorized): return status.Error(codes.Unauthenticated, "unauthenticated")
    case errors.Is(err, domain.ErrForbidden):    return status.Error(codes.PermissionDenied, "forbidden")
    case errors.Is(err, domain.ErrValidation):   return status.Error(codes.InvalidArgument, sanitize(err))
    case errors.Is(err, domain.ErrRateLimit):    return status.Error(codes.ResourceExhausted, "rate limited")
    case errors.Is(err, context.DeadlineExceeded): return status.Error(codes.DeadlineExceeded, "deadline exceeded")
    default:
        slog.Error("unhandled internal error", "err", err)
        return status.Error(codes.Internal, "internal error")
    }
}

// sanitize: only expose validation messages; hide internal details for 5xx
func sanitize(err error) string {
    var ve *domain.ValidationError
    if errors.As(err, &ve) { return ve.Error() }
    return err.Error() // for client-safe errors (4xx)
}
```

---

## 7. Panic vs Error Policy

```go
// PANIC: only for programmer errors — invariant violations that should never happen
// Never panic for user input, network errors, or anything recoverable

// ✓ Panic-appropriate: violated internal contract
func mustPositive(n int, name string) {
    if n <= 0 { panic(fmt.Sprintf("internal: %s must be positive, got %d", name, n)) }
}

// ✓ Panic-appropriate: failed initialization that makes the program unusable
func MustConnect(dsn string) *pgxpool.Pool {
    pool, err := pgxpool.New(context.Background(), dsn)
    if err != nil { panic(fmt.Sprintf("MustConnect: failed to connect: %v", err)) }
    return pool
}

// ✗ Never panic in library code for user-triggered conditions:
// - invalid input → return error
// - network failure → return error
// - not found → return error

// Recovery at the outermost boundary (HTTP handler, goroutine root)
func recoverMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if p := recover(); p != nil {
                slog.ErrorContext(r.Context(), "panic recovered",
                    "panic", p, "stack", string(debug.Stack()))
                http.Error(w, "internal error", http.StatusInternalServerError)
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

---

## Error Handling Checklist

- [ ] Every error either handled locally or wrapped with context and returned
- [ ] No `_` for errors without explicit `//nolint:errcheck // reason` comment
- [ ] Sentinel errors defined at package level for identity comparison
- [ ] Typed errors implement `Is(error) bool` to work with `errors.Is`
- [ ] All wrapping uses `%w` verb for `errors.Is`/`errors.As` to work through chain
- [ ] HTTP adapters map domain errors → HTTP status codes in one place
- [ ] gRPC adapters map domain errors → gRPC status codes in one place
- [ ] Internal errors (5xx) logged with full context, never exposed to client
- [ ] Client errors (4xx) return structured JSON with code + message
- [ ] Panic used ONLY for programmer errors / invariant violations in `internal/`
- [ ] Recovery middleware at HTTP handler and goroutine root only
- [ ] Multi-error: `errors.Join` for multiple simultaneous validation failures
