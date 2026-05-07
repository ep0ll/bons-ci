---
name: golang-solid
description: >
  SOLID principles strictly applied to Go: Single Responsibility, Open/Closed, Liskov Substitution,
  Interface Segregation, and Dependency Inversion — with concrete Go idioms, anti-patterns, and
  refactoring examples. Use for architecture decisions, package design, interface design, dependency
  management, and any structural code review. Always combine with design-patterns/SKILL.md and
  clean-code/SKILL.md for full architectural guidance.
---

# SOLID Principles in Go — Strict Application

## S — Single Responsibility Principle

> A type/function should have one reason to change.

### Anti-pattern: God struct
```go
// ✗ BAD — UserService does everything
type UserService struct { db *sql.DB; emailClient *smtp.Client; cache *redis.Client }

func (s *UserService) Register(email, password string) error {
    // validates, hashes password, saves to DB, sends email, caches session
    // FOUR reasons to change: validation rules, hashing algo, DB schema, email template
}
```

### Pattern: Focused types with single purpose
```go
// ✓ GOOD — each type has one job
type UserValidator struct{}
func (v *UserValidator) Validate(email, password string) error { ... }

type PasswordHasher struct{}
func (h *PasswordHasher) Hash(password string) (string, error) { ... }

type UserRepository interface {
    Save(ctx context.Context, user *User) error
}

type WelcomeEmailSender interface {
    Send(ctx context.Context, email string) error
}

// Orchestrator — its one job is to coordinate the above
type UserRegistrar struct {
    validator  *UserValidator
    hasher     *PasswordHasher
    repo       UserRepository
    emailer    WelcomeEmailSender
}

func (r *UserRegistrar) Register(ctx context.Context, email, password string) error {
    if err := r.validator.Validate(email, password); err != nil {
        return fmt.Errorf("validate: %w", err)
    }
    hash, err := r.hasher.Hash(password)
    if err != nil { return fmt.Errorf("hash: %w", err) }
    user := &User{Email: email, PasswordHash: hash}
    if err := r.repo.Save(ctx, user); err != nil {
        return fmt.Errorf("save: %w", err)
    }
    return r.emailer.Send(ctx, email)
}
```

### Package-level SRP
```
// Each package has one domain concept it owns:
internal/
├── user/       → user domain (entity, repository interface, service)
├── auth/       → authentication only
├── email/      → email sending only
├── cache/      → caching abstraction
└── store/      → persistence implementations
```

---

## O — Open/Closed Principle

> Open for extension, closed for modification.

### Anti-pattern: Switch on type
```go
// ✗ BAD — adding a new payment type requires modifying this function
func ProcessPayment(method string, amount float64) error {
    switch method {
    case "credit_card": return processCard(amount)
    case "paypal":      return processPayPal(amount)
    // must modify to add "crypto"
    }
    return fmt.Errorf("unknown method: %s", method)
}
```

### Pattern: Behavior through interfaces + registration
```go
// ✓ GOOD — new payment methods extend without modification
type PaymentProcessor interface {
    Process(ctx context.Context, amount Money) (*Receipt, error)
    Supports(method string) bool
}

type PaymentRouter struct {
    processors []PaymentProcessor
}

func (r *PaymentRouter) Register(p PaymentProcessor) {
    r.processors = append(r.processors, p)
}

func (r *PaymentRouter) Route(ctx context.Context, method string, amount Money) (*Receipt, error) {
    for _, p := range r.processors {
        if p.Supports(method) { return p.Process(ctx, amount) }
    }
    return nil, fmt.Errorf("no processor for method %q", method)
}

// New payment method = new file, zero changes to router:
type CryptoProcessor struct{ ... }
func (c *CryptoProcessor) Supports(m string) bool  { return m == "crypto" }
func (c *CryptoProcessor) Process(ctx context.Context, a Money) (*Receipt, error) { ... }
```

### Functional extension with options
```go
// Extend behavior without changing constructor
type Pipeline[T any] struct {
    steps []func(context.Context, T) (T, error)
}

func (p *Pipeline[T]) AddStep(fn func(context.Context, T) (T, error)) *Pipeline[T] {
    p.steps = append(p.steps, fn)
    return p
}

func (p *Pipeline[T]) Execute(ctx context.Context, input T) (T, error) {
    current := input
    for _, step := range p.steps {
        var err error
        if current, err = step(ctx, current); err != nil { return current, err }
    }
    return current, nil
}
```

---

## L — Liskov Substitution Principle

> Subtypes must be substitutable for their base types without altering correctness.

In Go: **any value that implements an interface must satisfy ALL the behavioral contracts of that interface**, not just the method signatures.

### Anti-pattern: Surprise behavior
```go
// ✗ BAD — ReadOnlyCache claims to implement Cache but panics on writes
type ReadOnlyCache struct{ data map[string]any }
func (c *ReadOnlyCache) Get(k string) (any, bool) { return c.data[k], true }
func (c *ReadOnlyCache) Set(k string, v any) { panic("read-only!") } // LSP violation!
```

### Pattern: Segregated interfaces that match capabilities
```go
// ✓ GOOD — separate interfaces for separate capabilities
type CacheReader interface { Get(ctx context.Context, key string) (any, bool, error) }
type CacheWriter interface { Set(ctx context.Context, key string, value any, ttl time.Duration) error }
type Cache interface { CacheReader; CacheWriter }

// ReadOnlyCache only implements CacheReader — LSP is preserved
type ReadOnlyCache struct{ data map[string]any }
func (c *ReadOnlyCache) Get(_ context.Context, key string) (any, bool, error) {
    v, ok := c.data[key]
    return v, ok, nil
}
// Does NOT implement CacheWriter — correct and honest

// Code that only reads accepts CacheReader, not Cache
func LookupUser(ctx context.Context, cache CacheReader, id string) (*User, error) { ... }
```

### Preconditions/postconditions
```go
// Implementations must not strengthen preconditions or weaken postconditions
// Document contracts in interface godoc:
// Repository.FindByID MUST return ErrNotFound when no record exists — never nil, nil
type Repository interface {
    // FindByID returns the entity with the given ID.
    // Returns ErrNotFound if no entity exists with that ID.
    // Never returns (nil, nil).
    FindByID(ctx context.Context, id ID) (*Entity, error)
}
```

---

## I — Interface Segregation Principle

> Clients should not depend on methods they don't use.

### Anti-pattern: Fat interface
```go
// ✗ BAD — every consumer gets methods they don't need
type Storage interface {
    Read(key string) ([]byte, error)
    Write(key string, val []byte) error
    Delete(key string) error
    List(prefix string) ([]string, error)
    Stats() StorageStats
    Backup(dst io.Writer) error
    Restore(src io.Reader) error
    Compact() error
}
```

### Pattern: Role-based interface slices
```go
// ✓ GOOD — define only what each consumer needs
type Getter  interface { Get(ctx context.Context, key string) ([]byte, error) }
type Setter  interface { Set(ctx context.Context, key string, val []byte) error }
type Deleter interface { Delete(ctx context.Context, key string) error }
type Lister  interface { List(ctx context.Context, prefix string) ([]string, error) }

// Compose as needed
type ReadStore  interface { Getter; Lister }
type WriteStore interface { Setter; Deleter }
type Store      interface { ReadStore; WriteStore }

// Consumers depend only on what they use:
func (s *SearchService) Search(ctx context.Context, q string) ([]string, error) {
    // only needs Lister — not the whole Store
    return s.lister.List(ctx, q)
}
```

---

## D — Dependency Inversion Principle

> High-level modules should not depend on low-level modules. Both should depend on abstractions.

### Anti-pattern: Concrete dependencies
```go
// ✗ BAD — OrderService is tightly coupled to MySQL and SMTP
type OrderService struct {
    db     *sql.DB           // concrete MySQL
    mailer *smtp.Client      // concrete SMTP
}
```

### Pattern: Constructor injection with interfaces
```go
// ✓ GOOD — define abstractions in the domain layer
// (interfaces live with the consumer, not the implementation)

// domain/order/ports.go
type OrderRepository interface {
    Save(ctx context.Context, o *Order) error
    FindByID(ctx context.Context, id OrderID) (*Order, error)
}
type OrderNotifier interface {
    NotifyConfirmed(ctx context.Context, o *Order) error
}

// domain/order/service.go
type OrderService struct {
    repo     OrderRepository  // interface, not *sql.DB
    notifier OrderNotifier    // interface, not *smtp.Client
}

func NewOrderService(repo OrderRepository, notifier OrderNotifier) *OrderService {
    return &OrderService{repo: repo, notifier: notifier}
}

// adapters/postgres/order_repo.go — implementation detail
type postgresOrderRepo struct { db *sql.DB }
func (r *postgresOrderRepo) Save(ctx context.Context, o *Order) error { ... }

// Wire it all in main.go (composition root)
func main() {
    db := mustOpenDB()
    repo := postgres.NewOrderRepo(db)
    mailer := smtp.NewMailer(smtpConfig)
    svc := order.NewOrderService(repo, mailer)
    // ...
}
```

### The Composition Root Rule
```
// ALL dependency wiring happens in ONE place: main.go or wire.go
// No package should call another package's constructor except main
// This makes the dependency graph explicit and testable
```

---

## SOLID Review Checklist

- [ ] **S**: Each type/function has exactly one reason to change — can I name it?
- [ ] **S**: Package names are nouns (domain concepts), not verbs
- [ ] **O**: New behaviors added via new types/files, not editing existing switch statements
- [ ] **O**: Configuration uses functional options or registries, not if-else chains
- [ ] **L**: All interface implementations honor preconditions and postconditions
- [ ] **L**: Interfaces never panic on optional methods — segregate instead
- [ ] **I**: No interface has more methods than its smallest consumer needs
- [ ] **I**: Check: would a mock/test double need to implement unused stubs? → split the interface
- [ ] **D**: High-level packages import only interfaces, never concrete adapters
- [ ] **D**: All wiring is in `main` or a dedicated composition root
- [ ] **D**: Interfaces are defined in the consuming package (not the providing package)
