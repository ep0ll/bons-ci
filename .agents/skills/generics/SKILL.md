---
name: golang-generics
description: >
  Advanced Go generics (Go 1.18+): type parameters, constraints, type inference, generic
  collections, generic algorithms, generic result/option types, when to use generics vs
  interfaces, and performance implications. Cross-references: golang-core/SKILL.md, dsa/SKILL.md.
---

# Go Generics — Advanced Patterns

## 1. Constraint Design

```go
import "golang.org/x/exp/constraints"

// Built-in constraints
constraints.Ordered   // ~int | ~int8 | ... | ~float64 | ~string
constraints.Integer   // ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint...
constraints.Float     // ~float32 | ~float64
constraints.Signed    // signed integers
constraints.Unsigned  // unsigned integers

// Custom constraints
type Number interface {
    constraints.Integer | constraints.Float
}

type Stringer interface {
    String() string
}

// Interface constraint with methods AND type set
type Numeric interface {
    ~int | ~int32 | ~int64 | ~float32 | ~float64
    // Methods can be added:
    // IsZero() bool
}

// Union of interfaces
type Serializable interface {
    encoding.TextMarshaler | json.Marshaler
}
```

## 2. Generic Collections

```go
// Optional[T] — explicit nullability without pointers
type Optional[T any] struct {
    value T
    valid bool
}

func Some[T any](v T) Optional[T]      { return Optional[T]{value: v, valid: true} }
func None[T any]() Optional[T]         { return Optional[T]{} }
func (o Optional[T]) IsPresent() bool  { return o.valid }
func (o Optional[T]) Get() (T, bool)   { return o.value, o.valid }
func (o Optional[T]) OrElse(def T) T   { if o.valid { return o.value }; return def }
func (o Optional[T]) OrElseGet(fn func() T) T {
    if o.valid { return o.value }
    return fn()
}
func (o Optional[T]) IfPresent(fn func(T)) {
    if o.valid { fn(o.value) }
}

// Result[T] — explicit error handling without panic
type Result[T any] struct {
    value T
    err   error
}

func Ok[T any](v T) Result[T]       { return Result[T]{value: v} }
func Err[T any](err error) Result[T] { return Result[T]{err: err} }
func (r Result[T]) IsOk() bool       { return r.err == nil }
func (r Result[T]) Unwrap() (T, error) { return r.value, r.err }
func (r Result[T]) MustGet() T {
    if r.err != nil { panic(fmt.Sprintf("Result.MustGet on error: %v", r.err)) }
    return r.value
}
func (r Result[T]) Map(fn func(T) T) Result[T] {
    if r.err != nil { return r }
    return Ok(fn(r.value))
}

// Set[T] — O(1) membership test
type Set[T comparable] struct {
    m map[T]struct{}
}

func NewSet[T comparable](items ...T) Set[T] {
    s := Set[T]{m: make(map[T]struct{}, len(items))}
    for _, item := range items { s.Add(item) }
    return s
}

func (s *Set[T]) Add(item T)            { s.m[item] = struct{}{} }
func (s *Set[T]) Remove(item T)         { delete(s.m, item) }
func (s *Set[T]) Contains(item T) bool  { _, ok := s.m[item]; return ok }
func (s *Set[T]) Len() int              { return len(s.m) }
func (s *Set[T]) Items() []T {
    items := make([]T, 0, len(s.m))
    for k := range s.m { items = append(items, k) }
    return items
}
func (s *Set[T]) Intersect(other Set[T]) Set[T] {
    result := NewSet[T]()
    for k := range s.m {
        if other.Contains(k) { result.Add(k) }
    }
    return result
}
```

## 3. Generic Algorithms

```go
// Map, Filter, Reduce — type-safe, no interface boxing
func Map[T, U any](s []T, f func(T) U) []U {
    out := make([]U, len(s))
    for i, v := range s { out[i] = f(v) }
    return out
}

func Filter[T any](s []T, pred func(T) bool) []T {
    out := make([]T, 0, len(s))
    for _, v := range s {
        if pred(v) { out = append(out, v) }
    }
    return out
}

func Reduce[T, Acc any](s []T, init Acc, f func(Acc, T) Acc) Acc {
    acc := init
    for _, v := range s { acc = f(acc, v) }
    return acc
}

func Find[T any](s []T, pred func(T) bool) (T, bool) {
    for _, v := range s {
        if pred(v) { return v, true }
    }
    var zero T
    return zero, false
}

func GroupBy[T any, K comparable](s []T, key func(T) K) map[K][]T {
    m := make(map[K][]T)
    for _, v := range s { k := key(v); m[k] = append(m[k], v) }
    return m
}

func Chunk[T any](s []T, size int) [][]T {
    if size <= 0 { panic("chunk size must be positive") }
    chunks := make([][]T, 0, (len(s)+size-1)/size)
    for i := 0; i < len(s); i += size {
        end := min(i+size, len(s))
        chunks = append(chunks, s[i:end])
    }
    return chunks
}

// Keys and Values for maps
func Keys[K comparable, V any](m map[K]V) []K {
    keys := make([]K, 0, len(m))
    for k := range m { keys = append(keys, k) }
    return keys
}

func Values[K comparable, V any](m map[K]V) []V {
    vals := make([]V, 0, len(m))
    for _, v := range m { vals = append(vals, v) }
    return vals
}
```

## 4. Generic Repository Base

```go
// Reduce boilerplate across multiple repository implementations
type BaseRepo[Entity any, ID comparable] struct {
    pool  *pgxpool.Pool
    table string
    scan  func(pgx.CollectableRow) (Entity, error)
}

func (r *BaseRepo[E, ID]) FindByID(ctx context.Context, id ID) (*E, error) {
    rows, err := r.pool.Query(ctx,
        fmt.Sprintf(`SELECT * FROM %s WHERE id = $1 AND deleted_at IS NULL`, r.table), id)
    if err != nil { return nil, fmt.Errorf("%s.FindByID: %w", r.table, err) }

    entity, err := pgx.CollectOneRow(rows, r.scan)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) { return nil, domain.ErrNotFound }
        return nil, fmt.Errorf("%s.FindByID.scan: %w", r.table, err)
    }
    return &entity, nil
}
```

## 5. Generic Cache

```go
type Cache[K comparable, V any] struct {
    mu    sync.RWMutex
    items map[K]cacheItem[V]
    ttl   time.Duration
}

type cacheItem[V any] struct {
    value   V
    expires time.Time
}

func NewCache[K comparable, V any](ttl time.Duration) *Cache[K, V] {
    return &Cache[K, V]{items: make(map[K]cacheItem[V]), ttl: ttl}
}

func (c *Cache[K, V]) Get(key K) (V, bool) {
    c.mu.RLock(); defer c.mu.RUnlock()
    item, ok := c.items[key]
    if !ok || time.Now().After(item.expires) {
        var zero V; return zero, false
    }
    return item.value, true
}

func (c *Cache[K, V]) Set(key K, value V) {
    c.mu.Lock(); defer c.mu.Unlock()
    c.items[key] = cacheItem[V]{value: value, expires: time.Now().Add(c.ttl)}
}
```

## 6. When Generics vs Interfaces

```go
// USE GENERICS when:
//   - You need type-safe collections (Map, Filter, Stack, Queue)
//   - You need compile-time type checking across different types
//   - You want to eliminate interface{} type assertions
//   - Same algorithm works for multiple types (Sort, Min, Max)

// USE INTERFACES when:
//   - Different behaviors are needed per type (polymorphism)
//   - You need runtime type switching
//   - Working with external packages that use interfaces
//   - Testability via mocking

// ✗ Over-using generics — interfaces are cleaner here
func ProcessGeneric[T interface{ Process() error }](item T) error {
    return item.Process()
}
// ✓ Interface is cleaner
type Processor interface { Process() error }
func Process(p Processor) error { return p.Process() }

// ✓ Generics shine for data transforms
func ToMap[T any, K comparable](slice []T, key func(T) K) map[K]T {
    m := make(map[K]T, len(slice))
    for _, v := range slice { m[key(v)] = v }
    return m
}
```

## Generics Checklist
- [ ] Type constraints use `~T` for underlying type matching (not just `T`)
- [ ] `comparable` constraint for map keys and equality operations
- [ ] `any` constraint when type doesn't need methods or comparison
- [ ] Zero value handled correctly in generic functions (`var zero T; return zero, false`)
- [ ] Generic functions infer types from arguments — rarely need explicit `[T]` at call site
- [ ] Benchmarked vs interface version — generics may or may not be faster (measure!)
- [ ] Generics not used where interfaces + polymorphism are clearer
- [ ] `constraints` package from `golang.org/x/exp` for numeric constraints
