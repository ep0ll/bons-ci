---
name: pkg-lo
description: >
  Exhaustive reference for samber/lo: generic functional utilities for Go — Map, Filter,
  Reduce, Find, Contains, Uniq, GroupBy, Chunk, Flatten, Keys, Values, and more.
  Use to replace verbose range loops with expressive, type-safe functional transforms.
  Cross-references: golang-core/SKILL.md (generics).
---

# Package: samber/lo — Complete Reference

## Import
```go
import "github.com/samber/lo"
```

## 1. Slice Operations

```go
// Map: transform each element
names := lo.Map(users, func(u User, _ int) string { return u.Name })
ids   := lo.Map(orders, func(o Order, _ int) string { return o.ID })

// Filter: keep elements matching predicate
active  := lo.Filter(users, func(u User, _ int) bool { return u.Status == "active" })
pending := lo.Filter(orders, func(o Order, _ int) bool { return o.Status == "pending" })

// Reduce: aggregate into single value
total := lo.Reduce(orders, func(acc int64, o Order, _ int) int64 {
    return acc + o.Total
}, 0)

// Find: first match
admin, found := lo.Find(users, func(u User) bool { return u.Role == "admin" })
// found=false if not found — admin is zero value

// Contains
hasAdmin := lo.Contains(roles, "admin")
hasAny   := lo.ContainsBy(users, func(u User) bool { return u.Role == "admin" })

// Uniq: deduplicate
uniqueIDs := lo.Uniq(ids)
uniqueBy  := lo.UniqBy(users, func(u User) string { return u.Email })

// Flatten: [][]T → []T
flat := lo.Flatten([][]string{{"a", "b"}, {"c", "d"}})

// Chunk: split into batches of N
batches := lo.Chunk(items, 100) // [][]Item with max 100 per batch

// Compact: remove zero values
nonEmpty := lo.Compact([]string{"", "a", "", "b"}) // ["a","b"]
nonNil   := lo.Compact([]*User{nil, user1, nil})   // [user1]

// Reverse
reversed := lo.Reverse(items)

// Shuffle
shuffled := lo.Shuffle(items)

// Sample / Samples
one  := lo.Sample(items)       // one random element
five := lo.Samples(items, 5)   // 5 random elements
```

## 2. Map Operations

```go
// Keys, Values
keys   := lo.Keys(myMap)
values := lo.Values(myMap)

// Invert: swap keys and values (values must be comparable)
inverted := lo.Invert(map[string]int{"a": 1, "b": 2}) // {1:"a", 2:"b"}

// Entries: map → slice of key-value pairs
entries := lo.Entries(myMap) // []lo.Entry[K,V]{Key, Value}

// FromEntries: reverse of Entries
m := lo.FromEntries(entries)

// MapKeys, MapValues: transform keys or values
upper  := lo.MapKeys(m, func(v int, k string) string { return strings.ToUpper(k) })
doubled := lo.MapValues(m, func(v int, k string) int { return v * 2 })

// PickBy / OmitBy: filter map by predicate
active := lo.PickBy(usersMap, func(id string, u User) bool { return u.Active })
sans   := lo.OmitBy(usersMap, func(id string, u User) bool { return u.Deleted })

// Assign: merge maps (right overwrites left)
merged := lo.Assign(defaults, overrides)
```

## 3. GroupBy, Partition, Associate

```go
// GroupBy: group slice by key
byStatus := lo.GroupBy(orders, func(o Order) string { return o.Status })
// map[string][]Order{"pending": [...], "confirmed": [...]}

// Partition: split into pass/fail
active, inactive := lo.Partition(users, func(u User) bool { return u.Active })

// Associate: slice → map
byID := lo.Associate(users, func(u User) (string, User) { return u.ID, u })

// SliceToMap: alias for Associate
index := lo.SliceToMap(users, func(u User) (string, *User) { return u.ID, &u })
```

## 4. Error Handling Helpers

```go
// Must: panic if error — for initialization only
db := lo.Must(sql.Open("pgx", dsn))  // panics if error

// Must1, Must2... for multi-return functions
value := lo.Must1(strconv.Atoi("42")) // panics if error

// Try: run func, return (ok bool) — for optional operations
ok := lo.Try(func() error { return riskyOp() })

// TryCatch: run with error handler
lo.TryCatch(func() error {
    return riskyOp()
}, func(err error) {
    slog.Error("operation failed", "err", err)
})
```

## 5. Pointer Helpers

```go
// ToPtr: get pointer to value (very common need)
ptr := lo.ToPtr("hello")     // *string
ptr  = lo.ToPtr(42)          // *int
ptr  = lo.ToPtr(time.Now())  // *time.Time

// FromPtr: dereference with fallback
val := lo.FromPtr(ptr)        // zero value if nil
val  = lo.FromPtrOr(ptr, "default")

// ToSlicePtr: []*T from []T
ptrs := lo.ToSlicePtr(items)
```

## 6. Intersection, Union, Difference

```go
// Intersect: common elements
common := lo.Intersect(slice1, slice2)

// Union: all unique elements from both
all := lo.Union(slice1, slice2)

// Difference: elements in slice1 but not slice2
diff, extra := lo.Difference(slice1, slice2)
// diff: in slice1 but not slice2; extra: in slice2 but not slice1
```

## 7. Ternary and Coalesce

```go
// Ternary: inline if-else expression
result := lo.Ternary(condition, "yes", "no")
value  := lo.TernaryF(condition,
    func() string { return computeA() },
    func() string { return computeB() },
)

// Coalesce: first non-zero value
name := lo.Coalesce("", "", "Alice", "Bob") // "Alice"
val  := lo.CoalesceOrEmpty(nilPtr, otherPtr)
```

## lo Usage Guidelines

```go
// ✓ USE lo when: transforming data structures, replacing verbose range loops
names := lo.Map(users, func(u User, _ int) string { return u.Name })
// vs:
names := make([]string, len(users))
for i, u := range users { names[i] = u.Name }

// ✓ USE lo.ToPtr for pointer-to-literal (very common in tests/config)
req.Name = lo.ToPtr("Alice")

// ✗ DON'T USE lo in hot paths if allocations matter — lo functions allocate new slices
// Profile first; if allocations are a problem, use manual loops

// ✓ USE lo.Must ONLY in main/init for truly unrecoverable initialization
db := lo.Must(sql.Open(...))  // ✓ in main
result := lo.Must(compute())  // ✗ in library/service code — return errors instead
```

## lo Checklist
- [ ] `lo.Must` only in `main()` or `TestMain` — never in library/service code
- [ ] `lo.Map` / `lo.Filter` preferred over manual slice building in non-hot paths
- [ ] `lo.Find` checked for `found` bool — never assume found=true
- [ ] `lo.GroupBy` for building in-memory indexes from slices
- [ ] `lo.Chunk` for batch processing large slices
- [ ] `lo.ToPtr` for pointer-to-literal in tests and optional struct fields
- [ ] `lo.Compact` for removing nil/zero elements after transformation
- [ ] Performance-critical paths: benchmark lo vs manual loop before committing
