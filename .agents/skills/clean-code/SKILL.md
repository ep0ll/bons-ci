---
name: golang-clean-code
description: >
  Comprehensive Clean Code for Go — Uncle Bob's complete canon (all chapters, all heuristics
  N1-N7, F1-F4, C1-C5, G1-G36, T1-T9) strictly applied to Go idioms. Covers naming, functions,
  comments, formatting, objects, error handling, boundaries, unit tests, classes, systems,
  emergence, concurrency, and the full smell/heuristic reference. Mandatory on every file.
  Load alongside solid-principles/SKILL.md for full architectural coverage.
---

# Clean Code (Uncle Bob) — Complete Go Implementation

> **Core Directive**: You are a craftsperson, not a code generator. Every line must survive
> scrutiny at 2 AM. If it requires mental translation → not clean. If it hides errors → dangerous.
> If clever but opaque → debt. **Clean code is honest code.**

---

## 🎯 Pre-Flight Checklist (Before Writing Any Code)

```
□ Clarify requirements — identify all domain concepts first
□ Draft interfaces/contracts before implementations (DIP)
□ Sketch test cases mentally (F.I.R.S.T.)
□ Choose error strategy: wrap | sentinel | typed
□ Plan concurrency boundaries (if applicable)
□ Apply Step-Down Rule plan: public API → helpers → utilities
```

---

## Chapter 2: Meaningful Names — All 17 Rules

### N1: Use Intention-Revealing Names
```go
// ✗ BAD — what is d? what does it do?
var d int
func getThem() [][]int { ... }

// ✓ GOOD — answers what, why, how
var elapsedTimeInDays int
func getFlaggedCells() []Cell { ... }
```

### N2: Avoid Disinformation
```go
// ✗ BAD — accountList is not a list (it's a map); hp is ambiguous
var accountList map[string]Account
var hp float64

// ✓ GOOD
var accountsByID map[string]Account
var hypotenuse float64

// ✗ BAD — l looks like 1; O looks like 0
for l := 1; l < 10; l++ { }
var O int = 0

// ✓ GOOD — use full names in non-trivial scopes
for lineCount := 1; lineCount < 10; lineCount++ { }
```

### N3: Make Meaningful Distinctions
```go
// ✗ BAD — what is the difference between these?
func getActiveAccount() *Account { }
func getActiveAccountInfo() *Account { }
func getActiveAccountData() *Account { }

// ✓ GOOD — each name explains what's different
func getActiveAccount() *Account { }           // full account aggregate
func getActiveAccountSummary() *AccountSummary { } // read model subset
```

### N4: Use Pronounceable Names
```go
// ✗ BAD — cannot be discussed in code review
type DtaRcrd102 struct {
    genymdhms time.Time
    modymdhms time.Time
    pszqint   string
}

// ✓ GOOD — can speak them aloud
type CustomerRecord struct {
    generationTimestamp time.Time
    modificationTimestamp time.Time
    recordID string
}
```

### N5: Use Searchable Names
```go
// ✗ BAD — magic number 5, magic string "e"
for j := 0; j < 5; j++ { }
const e = 2.71828

// ✓ GOOD — searchable, self-documenting
const maxRetryAttempts = 5
const eulerNumber = 2.71828
for attempt := 0; attempt < maxRetryAttempts; attempt++ { }
```

### N6: Avoid Encodings (No Hungarian Notation)
```go
// ✗ BAD — type/scope prefix adds noise; IDEs handle this
var strFirstName string
var iCount int
var m_balance float64
type IUserRepository interface { } // "I" prefix — Go has implicit interfaces

// ✓ GOOD
var firstName string
var count int
var balance float64
type UserRepository interface { }
```

### N7: Avoid Mental Mapping
```go
// ✗ BAD — requires translation: what is r? what is u?
for r := range results {
    u := process(r)
    save(u)
}

// ✓ GOOD — read like prose
for _, result := range queryResults {
    user := hydrateUser(result)
    repo.Save(ctx, user)
}
```

### N8: Class/Type Names — Nouns
```go
// ✗ BAD — Manager, Processor, Data, Info are noise words
type UserManager struct{ }
type DataProcessor struct{ }

// ✓ GOOD — noun that describes what it IS
type UserRegistrar struct{ }   // registers users
type OrderFulfiller struct{ }  // fulfills orders
type PaymentGateway struct{ }  // payment boundary
```

### N9: Method/Function Names — Verbs
```go
// ✗ BAD — not a verb, unclear action
func (u *User) Active() error { }
func email(u *User) { }

// ✓ GOOD — verb phrase describes action
func (u *User) Activate() error { }
func (n *Notifier) SendWelcomeEmail(ctx context.Context, u *User) error { }

// Accessors/mutators: is/has/can/get/set (Go uses field access mostly)
func (u *User) IsActive() bool     { return u.status == StatusActive }
func (u *User) HasPermission(p Permission) bool { return u.perms.Contains(p) }
```

### N10: One Word per Concept — Be Consistent
```go
// ✗ BAD — inconsistent verbs confuse readers
func (r *UserRepo) FetchByID(id ID) (*User, error) { }
func (r *OrderRepo) GetByID(id ID) (*Order, error) { }
func (r *ProductRepo) RetrieveByID(id ID) (*Product, error) { }

// ✓ GOOD — one verb for one concept across entire codebase
// DECISION: use FindByID for all repository lookups
func (r *UserRepo)    FindByID(ctx context.Context, id ID) (*User, error) { }
func (r *OrderRepo)   FindByID(ctx context.Context, id ID) (*Order, error) { }
func (r *ProductRepo) FindByID(ctx context.Context, id ID) (*Product, error) { }
```

### N11: Don't Pun
```go
// ✗ BAD — Add means different things in different contexts
func (c *Collection) Add(item Item) { }     // adds to collection
func (m *Ledger) Add(amount Money) { }      // adds monetary value — confusing!

// ✓ GOOD — distinct verbs for distinct meanings
func (c *Collection) Append(item Item) { }
func (m *Ledger) Credit(amount Money) { }
```

### N12: Use Solution Domain Names (CS Terms)
```go
// ✓ OK when technical audience — pattern names, CS terms are clear
type OrderQueue struct{ }         // queue data structure
type UserVisitor interface{ }     // visitor pattern
type PaymentStrategy interface{ } // strategy pattern
type EventBroker struct{ }        // broker concept
```

### N13: Use Problem Domain Names (Business Terms)
```go
// ✓ GOOD when business-facing — use ubiquitous language
type Invoice struct{ }
type Shipment struct{ }
type CustomerSegment string
const SegmentPremium CustomerSegment = "premium"
```

### N14: Add Meaningful Context
```go
// ✗ BAD — firstName alone, no context
var firstName string    // part of an address? a person? a config?

// ✓ GOOD — context makes it clear
type Address struct {
    firstName string  // context from enclosing struct
    lastName  string
    street    string
    city      string
    state     string
    zip       string
}

// OR via function — every name in printAddress has context
func printGuessStatistics(candidate byte, count int) {
    var number, verb, pluralModifier string
    // context: all vars relate to guess statistics
    if count == 0 {
        number, verb, pluralModifier = "no", "are", "s"
    } else if count == 1 {
        number, verb, pluralModifier = "1", "is", ""
    } else {
        number = strconv.Itoa(count)
        verb, pluralModifier = "are", "s"
    }
    fmt.Printf("There %s %s %s%s\n", verb, number, string(candidate), pluralModifier)
}
```

### N15: Don't Add Gratuitous Context
```go
// ✗ BAD — prefix on every name in a package/app
type GSDAccountAddress struct{ }
type GSDAccountUser struct{ }

// ✓ GOOD — package provides context
package account
type Address struct{ }
type User struct{ }
```

### N16: Avoid Noise Words
```go
// ✗ BAD — Info, Data, Manager, Helper, Util add nothing
var theAccount Account
var productInfo Product
var dataManager *DataManager

// ✓ GOOD
var account Account
var product Product
var inventory *InventoryService
```

### N17: Receiver Names — Short, Consistent
```go
// Go-specific: receiver = short abbreviation of type, never self/this
// ✗ BAD
func (self *User) Validate() error { }
func (this *Order) Cancel() error  { }
func (userInstance *User) IsActive() bool { }

// ✓ GOOD — consistent single-letter or 2-letter abbreviation
func (u *User)  Validate() error    { }
func (o *Order) Cancel() error      { }
func (u *User)  IsActive() bool     { }
// Rule: ALL methods on a type must use the SAME receiver name
```

---

## Chapter 3: Functions — Complete Rules

### F1: Small — The First Rule
```go
// ✗ BAD — 80+ line function doing multiple things
func processUserRegistration(w http.ResponseWriter, r *http.Request) {
    // decode, validate, hash password, save to DB, send email,
    // create session, write response — all in one function
}

// ✓ GOOD — each function does ONE thing at ONE level of abstraction
func (h *UserHandler) Register(w http.ResponseWriter, r *http.Request) {
    req, err := h.decodeRegisterRequest(r)
    if err != nil { h.respondBadRequest(w, err); return }

    if err := h.validateRegistration(req); err != nil {
        h.respondUnprocessable(w, err); return
    }

    user, err := h.svc.Register(r.Context(), req)
    if err != nil { h.respondError(w, r, err); return }

    h.respondCreated(w, toUserView(user))
}
// 10 lines. Each called function is ONE level below.
```

### F2: Do One Thing
```go
// RULE: A function does ONE thing if you cannot meaningfully extract
// another function from it. If you can extract, it was doing two things.

// Test: "This function [name] [does X]" — if AND appears → two things

// ✗ TWO things: validates AND saves
func validateAndSaveUser(ctx context.Context, u *User) error {
    if u.Email == "" { return ErrEmptyEmail }
    return repo.Save(ctx, u) // validation + persistence — two things
}

// ✓ TWO separate concerns, two functions
func validateUser(u *User) error    { ... }  // validation only
func saveUser(ctx context.Context, u *User) error { ... }  // persistence only
```

### F3: One Level of Abstraction per Function
```go
// ✗ BAD — mixes high-level (renderPage) with low-level (string ops)
func renderPage(page *Page) string {
    var sb strings.Builder
    sb.WriteString("<html><body>")
    for _, section := range page.Sections {
        sb.WriteString("<div>")
        for _, line := range strings.Split(section.Content, "\n") {
            sb.WriteString("<p>" + html.EscapeString(line) + "</p>")
        }
        sb.WriteString("</div>")
    }
    sb.WriteString("</body></html>")
    return sb.String()
}

// ✓ GOOD — Step-Down Rule: each function one level below caller
func renderPage(page *Page) string {
    return renderHTML(renderBody(page))
}
func renderBody(page *Page) string {
    sections := make([]string, len(page.Sections))
    for i, s := range page.Sections { sections[i] = renderSection(s) }
    return strings.Join(sections, "")
}
func renderSection(s Section) string {
    return "<div>" + renderLines(s.Content) + "</div>"
}
func renderLines(content string) string {
    lines := strings.Split(content, "\n")
    paras := make([]string, len(lines))
    for i, l := range lines { paras[i] = "<p>" + html.EscapeString(l) + "</p>" }
    return strings.Join(paras, "")
}
```

### F4: The Step-Down Rule — Top-Down Narrative
```go
// File ordering: public functions first, helpers below
// Each function calls functions defined just below it

// Level 1 — highest abstraction (public)
func (s *OrderService) Place(ctx context.Context, cmd PlaceOrderCommand) (*Order, error) {
    if err := s.validateOrder(cmd); err != nil { return nil, err }
    order, err := s.buildOrder(cmd)
    if err != nil { return nil, err }
    return s.persistOrder(ctx, order)
}

// Level 2 — one step below
func (s *OrderService) validateOrder(cmd PlaceOrderCommand) error { ... }
func (s *OrderService) buildOrder(cmd PlaceOrderCommand) (*Order, error) { ... }
func (s *OrderService) persistOrder(ctx context.Context, o *Order) (*Order, error) { ... }

// Level 3 — implementation details (private)
func (s *OrderService) validateItems(items []Item) error { ... }
func (s *OrderService) calculateTotal(items []Item) Money { ... }
```

### F5: Function Arguments
```go
// Niladic (0 args) — best
func Now() time.Time { return time.Now() }

// Monadic (1 arg) — question about arg, or transform arg
func isValidEmail(email string) bool { ... }
func parseOrderID(raw string) (OrderID, error) { ... }

// Dyadic (2 args) — natural pair, same concept
func NewPoint(x, y float64) Point { ... }
func IsAfter(t1, t2 time.Time) bool { ... }

// Triadic (3 args) — use only when args are naturally ordered triple
// Most triads should become parameter objects

// ✗ BAD — triadic, args are not a natural ordered triple
func makeCircle(x, y, radius float64) Circle { ... }

// ✓ GOOD — parameter object reveals intent
type CircleSpec struct{ Center Point; Radius float64 }
func makeCircle(spec CircleSpec) Circle { ... }

// ✗ BAD — 5+ args — always use struct
func createUser(firstName, lastName, email, phone, role string, age int) *User { ... }

// ✓ GOOD
type CreateUserRequest struct {
    FirstName string `validate:"required,min=1,max=100"`
    LastName  string `validate:"required,min=1,max=100"`
    Email     string `validate:"required,email"`
    Phone     string `validate:"required,e164"`
    Role      string `validate:"required,oneof=admin user guest"`
    Age       int    `validate:"required,min=0,max=150"`
}
func createUser(ctx context.Context, req CreateUserRequest) (*User, error) { ... }
```

### F6: No Flag Arguments
```go
// ✗ BAD — bool arg changes behavior: function does TWO things
func render(page *Page, testMode bool) string {
    if testMode { return renderTest(page) }
    return renderProduction(page)
}

// ✓ GOOD — two explicit functions
func renderForTest(page *Page) string       { ... }
func renderForProduction(page *Page) string { ... }
```

### F7: No Output Arguments
```go
// ✗ BAD — output argument confuses callers (is s mutated?)
func appendFooter(s *string) { *s += "\n---footer---" }

// ✓ GOOD — return the result
func appendFooter(s string) string { return s + "\n---footer---" }
// If method must mutate state, do it on the receiver
func (d *Document) AppendFooter() { d.content += "\n---footer---" }
```

### F8: Command-Query Separation
```go
// ✗ BAD — sets attribute AND returns whether it was set (does two things)
func (m *Map) set(key string, val any) bool { ... }
if m.set("username", "alice") { ... } // confusing: did it succeed or was it already set?

// ✓ GOOD — separate command from query
func (m *Map) Set(key string, val any) { ... }       // command: no return
func (m *Map) Exists(key string) bool { ... }         // query: no side effect
func (m *Map) SetIfAbsent(key string, val any) bool { ... } // acceptable exception: named clearly
```

### F9: Prefer Exceptions/Errors to Error Codes
```go
// ✗ BAD — error codes: caller must check, easy to ignore
const (
    ErrCodeOK = 0
    ErrCodeNotFound = 1
    ErrCodeConflict = 2
)
func deleteUser(id string) int { // returns error code
    if !exists(id) { return ErrCodeNotFound }
    delete(id)
    return ErrCodeOK
}

// ✓ GOOD — typed errors: Go's idiomatic error handling
func (r *UserRepo) Delete(ctx context.Context, id UserID) error {
    n, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
    if err != nil { return fmt.Errorf("UserRepo.Delete(%s): %w", id, err) }
    if n == 0 { return domain.ErrNotFound }
    return nil
}
```

### F10: Extract Try/Catch Bodies
```go
// ✗ BAD — error handling mixed with business logic
func deleteUser(ctx context.Context, id string) {
    if err := repo.Delete(ctx, id); err != nil {
        // 10 lines of error handling interleaved with business logic
        log.Error("delete failed", "err", err)
        metrics.Inc("user.delete.error")
        notify(adminEmail, "User delete failed: "+err.Error())
    }
    audit.Log(ctx, "user.deleted", id)
    cache.Invalidate("user:"+id)
}

// ✓ GOOD — error handling is its own concern
func deleteUser(ctx context.Context, id string) error {
    if err := repo.Delete(ctx, id); err != nil {
        return handleDeleteError(ctx, id, err) // one line
    }
    return postDeleteCleanup(ctx, id) // one line
}
func handleDeleteError(ctx context.Context, id string, err error) error {
    slog.ErrorContext(ctx, "user delete failed", "id", id, "err", err)
    metrics.Inc("user.delete.error")
    notifyAdmin(ctx, "User delete failed: "+id)
    return fmt.Errorf("deleteUser(%s): %w", id, err)
}
func postDeleteCleanup(ctx context.Context, id string) error {
    audit.Log(ctx, "user.deleted", id)
    return cache.Invalidate(ctx, "user:"+id)
}
```

### F11: Don't Repeat Yourself (DRY)
```go
// ✗ BAD — pagination logic duplicated in 5 handler functions
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
    limitStr := r.URL.Query().Get("limit")
    limit, _ := strconv.Atoi(limitStr)
    if limit <= 0 || limit > 100 { limit = 20 }
    cursor := r.URL.Query().Get("cursor")
    // ... same 3 lines repeated in ListOrders, ListProducts, etc.
}

// ✓ GOOD — one place
func parsePaginationParams(r *http.Request) (limit int, cursor string) {
    limitStr := r.URL.Query().Get("limit")
    limit, _ = strconv.Atoi(limitStr)
    if limit <= 0 || limit > 100 { limit = 20 }
    return limit, r.URL.Query().Get("cursor")
}
```

### F12: Structured Programming (Early Returns > Nested Ifs)
```go
// ✗ BAD — arrow-shaped code (deep nesting)
func processOrder(ctx context.Context, order *Order) error {
    if order != nil {
        if order.IsValid() {
            if ctx.Err() == nil {
                if err := repo.Save(ctx, order); err == nil {
                    return notifyUser(ctx, order)
                } else { return err }
            } else { return ctx.Err() }
        } else { return ErrInvalidOrder }
    }
    return ErrNilOrder
}

// ✓ GOOD — guard clauses (early returns flatten the structure)
func processOrder(ctx context.Context, order *Order) error {
    if order == nil   { return ErrNilOrder }
    if !order.IsValid() { return ErrInvalidOrder }
    if err := ctx.Err(); err != nil { return err }

    if err := repo.Save(ctx, order); err != nil {
        return fmt.Errorf("processOrder.Save: %w", err)
    }
    return notifyUser(ctx, order)
}
```

---

## Chapter 4: Comments — The Complete Philosophy

> **Core Rule**: Every comment is a failure to express intent in code.
> Good code is self-documenting. Comments lie; code cannot.

### C1: Comments Do Not Make Up for Bad Code
```go
// ✗ BAD — comment compensating for bad code
// Check to see if the employee is eligible for full benefits
if (employee.flags & HOURLY_FLAG) > 0 && employee.age > 65 { }

// ✓ GOOD — the code explains itself
if employee.IsEligibleForFullBenefits() { }
```

### C2: Explain Yourself in Code, Not Comments
```go
// ✗ BAD — comment explains what the code should explain
// Add to the last position in the list
items = append(items, newItem)

// ✓ GOOD — if append is opaque, make an intention-revealing function
func (l *OrderList) AppendItem(item OrderItem) { l.items = append(l.items, item) }
```

### C3: Good Comments (The Only Acceptable Kinds)
```go
// 1. Legal/copyright
// Copyright 2024 Acme Corp. Licensed under MIT.

// 2. Informative — when code truly cannot explain
// Returns time formatted as kk:mm:ss EEE MMM dd yyyy
func formatTimestamp(t time.Time) string { return t.Format("15:04:05 Mon Jan 02 2006") }

// 3. Explanation of INTENT — WHY not WHAT
// We use a separate goroutine here because the payment gateway
// webhook timeout is 5s, but our audit log is eventually consistent.
// Blocking the webhook response on audit would cause false timeouts.
go func() { auditLog(ctx, event) }()

// 4. Clarification — when idiom is non-obvious
// Rotate token: atomic swap ensures no window where token is empty.
// Other goroutines calling currentToken() see either old or new, never nil.
old := atomic.SwapPointer(&h.token, unsafe.Pointer(newToken))

// 5. Warning of consequences
// WARNING: This deletes ALL user data. Only called in tests.
// Production path: use SoftDelete(ctx, id) instead.
func (r *UserRepo) hardDelete(ctx context.Context, id UserID) error { ... }

// 6. TODO — time-boxed, with ticket reference
// TODO(JIRA-1234): Replace with streaming implementation when user base > 1M
// Acceptable until Q2 2025.

// 7. Amplification — make non-obvious importance clear
// The trim is critical: leading/trailing spaces in currency codes
// cause silent failures in the payment gateway validation.
currency = strings.TrimSpace(currency)

// 8. Godoc on all exported symbols — mandatory
// UserRepository defines the persistence contract for the User aggregate.
// All implementations must honor the ErrNotFound sentinel for missing records.
type UserRepository interface { ... }
```

### C4: Bad Comments (Never Write These)
```go
// ✗ Mumbling — explains nothing
// This is for the case
if err != nil { return err }

// ✗ Redundant comment — says exactly what code says
// i is the integer loop counter
for i := 0; i < 10; i++ { }

// ✗ Misleading — wrong, or subtly inaccurate
// Returns true if user is admin
func (u *User) HasAccess(resource string) bool { ... } // actually checks resource-specific perms

// ✗ Mandated — team policy demanding comments on everything
// Constructor
func NewUser() *User { return &User{} }

// ✗ Journal comments — git history does this
// 2024-01-15 jsmith: Added null check
// 2024-01-10 bwong: Refactored loop

// ✗ Noise — says nothing useful
// Default constructor
func NewOrderService(repo Repository) *OrderService { return &OrderService{repo: repo} }

// ✗ Commented-out code — DELETE IT; git remembers
// oldValidation := validate(user)
// if oldValidation { ... }

// ✗ HTML/Markdown in non-godoc comments

// ✗ Too much information — implementation history in comments
// In 2019, we tried using UUID v1 but the timestamp ordering caused
// index fragmentation. In 2020, we switched to ULID but the 80-bit
// randomness was insufficient...

// ✗ Inobvious connection — comment refers to something else
// Make sure we fit in RSS (Really Simple Syndication? Resident Set Size? RSS reader?)
int realRssSize = ...
```

---

## Chapter 5: Formatting

### Vertical Formatting
```go
// 1. Vertical openness — blank lines separate concepts
type OrderService struct {
    repo      OrderRepository
    publisher EventPublisher
    logger    *slog.Logger
}
                                        // ← blank line: new concept
func NewOrderService(repo OrderRepository, pub EventPublisher, log *slog.Logger) *OrderService {
    return &OrderService{repo: repo, publisher: pub, logger: log}
}
                                        // ← blank line: new concept
func (s *OrderService) Place(ctx context.Context, cmd PlaceOrderCommand) (*Order, error) {
    if err := s.validate(cmd); err != nil {
        return nil, err
    }
                                        // ← blank line: separates phases
    order, err := order.New(cmd)
    if err != nil { return nil, fmt.Errorf("Place.New: %w", err) }
                                        // ← blank line
    if err := s.repo.Save(ctx, order); err != nil {
        return nil, fmt.Errorf("Place.Save: %w", err)
    }
    return order, nil
}

// 2. Vertical distance — related code stays close
// Variables declared near first use, NOT at top of function
func processOrder(ctx context.Context, order *Order) error {
    // ✗ BAD — declared far from use
    var taxRate float64
    var discount Money
    // ... 20 lines of unrelated code ...
    taxRate = getTaxRate(order.ShipTo)
    discount = calculateDiscount(order)

    // ✓ GOOD — declared at point of use
    taxRate := getTaxRate(order.ShipTo)
    discount := calculateDiscount(order)
}

// 3. Caller before callee — step-down order in file
// Public methods first, private helpers below
```

### Horizontal Formatting
```go
// Line length: ≤ 100-120 characters (gofmt handles most of this)
// Whitespace to show precedence
z := (-b + math.Sqrt(b*b-4*a*c)) / (2 * a)  // multiplication binds tighter → less space

// Alignment: gofmt does NOT align assignments — don't fight it
// ✗ WRONG (fights gofmt)
var firstName  string
var lastName   string
var emailAddr  string

// ✓ RIGHT (gofmt style)
var firstName string
var lastName string
var emailAddr string

// Exception: struct tag alignment in structs — acceptable
type User struct {
    ID        string `json:"id"         db:"id"`
    FirstName string `json:"first_name" db:"first_name"`
    Email     string `json:"email"      db:"email"`
}
```

---

## Chapter 6: Objects & Data Structures

### The Law of Demeter (Don't Talk to Strangers)
```go
// ✗ BAD — train wreck: talks to strangers (ctxt → Options → ScratchDir)
outputDir := ctxt.GetOptions().GetScratchDir().GetAbsolutePath()

// ✗ BAD even split across lines — still talking to strangers
opts := ctxt.GetOptions()
scratchDir := opts.GetScratchDir()
absPath := scratchDir.GetAbsolutePath()

// ✓ GOOD — tell, don't ask; ask the object to do work for you
outputDir := ctxt.GetScratchDirectoryPath() // ctxt knows its own scratch dir

// ✓ GOOD — data structures (DTOs) are exempt from Demeter
type Point struct{ X, Y float64 }
p := getPoint()
dist := math.Sqrt(p.X*p.X + p.Y*p.Y) // fine — Point is a data structure
```

### Data Abstraction
```go
// ✗ BAD — exposes implementation (concrete coordinates)
type Point struct {
    X float64
    Y float64
}

// ✓ GOOD — hides representation behind meaningful abstraction
type Point interface {
    GetX() float64
    GetY() float64
    SetCartesian(x, y float64)
    GetR() float64       // polar radius
    GetTheta() float64   // polar angle
    SetPolar(r, theta float64)
}
```

### Data/Object Anti-Symmetry
```go
// Procedural: easy to ADD functions, hard to ADD types
// Use when types are stable, operations change frequently
type Square struct{ Side float64 }
type Circle struct{ Radius float64 }
type Triangle struct{ Base, Height float64 }

func Area(shape any) float64 {
    switch s := shape.(type) {
    case Square:   return s.Side * s.Side
    case Circle:   return math.Pi * s.Radius * s.Radius
    case Triangle: return 0.5 * s.Base * s.Height
    }
    panic("unknown shape")
}
// Adding Area is easy. Adding Rectangle requires editing Area.

// OO polymorphic: easy to ADD types, hard to ADD functions
// Use when functions are stable, types change frequently
type Shape interface { Area() float64 }
type Square   struct{ Side float64   }; func (s Square) Area() float64 { return s.Side * s.Side }
type Circle   struct{ Radius float64 }; func (c Circle) Area() float64 { return math.Pi * c.Radius * c.Radius }
// Adding Rectangle is easy. Adding Perimeter() requires editing all types.
```

---

## Chapter 7: Error Handling — Complete Rules

### E1: Use Error Returns, Not Error Codes
```go
// ✗ BAD — Go has no exceptions; don't simulate error codes
const (ErrOK = 0; ErrFail = 1; ErrNotFound = 2)
func deleteUser(id string) int { ... }

// ✓ GOOD — errors are values, returned explicitly
func (r *UserRepo) Delete(ctx context.Context, id UserID) error { ... }
```

### E2: Write the Happy Path First, Handle Errors Inline
```go
// ✓ GOOD — happy path is clear, errors handled immediately
func (s *OrderService) Cancel(ctx context.Context, id OrderID) error {
    order, err := s.repo.FindByID(ctx, id)
    if err != nil { return fmt.Errorf("Cancel.FindByID: %w", err) }

    if err := order.Cancel(); err != nil {
        return fmt.Errorf("Cancel.order.Cancel: %w", err)
    }

    if err := s.repo.Save(ctx, order); err != nil {
        return fmt.Errorf("Cancel.Save: %w", err)
    }
    return nil
}
```

### E3: Never Return Nil — Use Sentinel or ErrNotFound
```go
// ✗ BAD — returning (nil, nil) is a lie; caller can't tell success from "not found"
func (r *Repo) FindByID(id string) (*User, error) {
    row := r.db.QueryRow(...)
    if row == nil { return nil, nil } // BAD
    // ...
}

// ✓ GOOD — explicit: not found is a named error
var ErrNotFound = errors.New("not found")
func (r *Repo) FindByID(ctx context.Context, id string) (*User, error) {
    // ...
    if errors.Is(err, sql.ErrNoRows) { return nil, ErrNotFound }
    // ...
}
```

### E4: Don't Pass Nil, Don't Return Nil
```go
// ✗ BAD — nil passed as argument silently causes panic later
processor.Process(nil, config)

// ✓ GOOD — validate at boundaries
func (p *Processor) Process(ctx context.Context, input *Input) error {
    if input == nil { return errors.New("Process: input must not be nil") }
    // ...
}

// ✓ GOOD — return zero value instead of nil for non-error cases
func getUser() User {
    if notFound { return User{} } // zero value, not nil pointer
}
```

### E5: Wrap Errors with Context
```go
// ✗ BAD — context lost, impossible to trace
return err

// ✗ BAD — %v loses error type for errors.Is/As
return fmt.Errorf("something went wrong: %v", err)

// ✓ GOOD — %w preserves type for errors.Is/As + adds call context
return fmt.Errorf("UserService.Register(email=%q): %w", email, err)
```

### E6: Define Error Taxonomy (Sentinel + Typed)
```go
// Sentinel errors — for identity comparison
var (
    ErrNotFound   = errors.New("not found")
    ErrConflict   = errors.New("version conflict")
    ErrValidation = errors.New("validation failed")
)

// Typed errors — for structured context
type ValidationError struct {
    Field   string
    Value   any
    Message string
}
func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation: %s=%v: %s", e.Field, e.Value, e.Message)
}
func (e *ValidationError) Is(target error) bool {
    _, ok := target.(*ValidationError); return ok
}

// Checking
if errors.Is(err, ErrNotFound) { /* 404 */ }
var ve *ValidationError
if errors.As(err, &ve) { /* 422, ve.Field, ve.Message */ }
```

---

## Chapter 8: Boundaries

```go
// ✗ BAD — third-party type leaked throughout codebase
type OrderService struct {
    kafkaProducer *sarama.SyncProducer // sarama leaked into domain
}
func (s *OrderService) Publish(msg *sarama.ProducerMessage) error { ... }

// ✓ GOOD — wrap third-party behind interface at boundary
type EventPublisher interface {
    Publish(ctx context.Context, topic string, key string, payload []byte) error
}

type kafkaPublisher struct{ producer sarama.SyncProducer }
func (k *kafkaPublisher) Publish(ctx context.Context, topic, key string, payload []byte) error {
    msg := &sarama.ProducerMessage{
        Topic: topic,
        Key:   sarama.StringEncoder(key),
        Value: sarama.ByteEncoder(payload),
    }
    _, _, err := k.producer.SendMessage(msg)
    return err
}

// Only kafkaPublisher knows about sarama. Domain knows only EventPublisher.
```

---

## Chapter 9: Unit Tests — F.I.R.S.T.

```go
// F — Fast: must run in milliseconds; mock all I/O
// I — Independent: no shared state; each test runs alone
// R — Repeatable: same result every time; no time/random dependencies
// S — Self-Validating: pass or fail; no manual inspection
// T — Timely: written before production code (TDD) or alongside

// ✓ Clean test structure: Build-Operate-Check (AAA)
func TestOrderService_Place_ValidInput_ReturnsConfirmedOrder(t *testing.T) {
    t.Parallel()

    // ARRANGE (Build)
    repo := mocks.NewMockOrderRepository(t)
    pub  := mocks.NewMockEventPublisher(t)
    svc  := NewOrderService(repo, pub, slog.Default())

    repo.EXPECT().
        Save(mock.Anything, mock.MatchedBy(func(o *Order) bool {
            return o.CustomerID() == "cust-1"
        })).Return(nil).Once()

    // ACT (Operate)
    result, err := svc.Place(context.Background(), PlaceOrderCommand{
        CustomerID: "cust-1",
        Items:      []Item{{ProductID: "p-1", Qty: 2}},
    })

    // ASSERT (Check)
    require.NoError(t, err)
    assert.Equal(t, "cust-1", result.CustomerID().String())
    assert.Equal(t, StatusPending, result.Status())
}

// One concept per test — split if multiple unrelated assertions
// ✗ BAD — two concepts in one test
func TestUser_ValidateAndSave(t *testing.T) {
    err := validate(user)
    assert.NoError(t, err)          // concept 1: validation
    err = repo.Save(ctx, user)
    assert.NoError(t, err)          // concept 2: persistence — different test!
}
```

### Table-Driven Tests (Go Idiomatic)
```go
func TestParseAmount(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name    string
        input   string
        want    Amount
        wantErr error
    }{
        {name: "valid integer",   input: "100",  want: Amount{Cents: 10000}},
        {name: "valid decimal",   input: "19.99", want: Amount{Cents: 1999}},
        {name: "zero",            input: "0",     want: Amount{Cents: 0}},
        {name: "negative",        input: "-5",    wantErr: ErrNegativeAmount},
        {name: "empty",           input: "",      wantErr: ErrEmptyAmount},
        {name: "overflow",        input: "99999999999", wantErr: ErrAmountOverflow},
        {name: "non-numeric",     input: "abc",   wantErr: ErrInvalidFormat},
    }
    for _, tt := range tests {
        tt := tt
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            got, err := ParseAmount(tt.input)
            if tt.wantErr != nil {
                require.ErrorIs(t, err, tt.wantErr)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

---

## Chapter 13: Concurrency — Clean Code Rules

```go
// CC1: Keep concurrency code separate from other code
// Isolate goroutine management; don't mix with business logic

// CC2: Limit scope of shared data
type SafeCounter struct {
    mu    sync.Mutex
    count int  // documented: guarded by mu
}
func (c *SafeCounter) Inc() { c.mu.Lock(); c.count++; c.mu.Unlock() }
func (c *SafeCounter) Val() int { c.mu.Lock(); defer c.mu.RUnlock(); return c.count }

// CC3: Use copies of data rather than sharing
func processItems(items []Item) []Result {
    itemsCopy := append([]Item(nil), items...)  // defensive copy
    results := make([]Result, len(itemsCopy))
    var wg sync.WaitGroup
    for i, item := range itemsCopy {
        i, item := i, item  // new binding per iteration (Go < 1.22)
        wg.Add(1)
        go func() {
            defer wg.Done()
            results[i] = process(item)  // writes separate indices — safe
        }()
    }
    wg.Wait()
    return results
}

// CC4: Use context for cancellation — not done channels directly
func worker(ctx context.Context, jobs <-chan Job) error {
    for {
        select {
        case job, ok := <-jobs:
            if !ok { return nil }
            if err := process(ctx, job); err != nil { return err }
        case <-ctx.Done():
            return fmt.Errorf("worker cancelled: %w", ctx.Err())
        }
    }
}

// CC5: Keep synchronized sections tiny
// ✗ BAD — DB query inside lock!
func (s *Store) GetOrFetch(key string) (Value, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if v, ok := s.cache[key]; ok { return v, nil }
    v, err := s.db.Query(key)  // I/O inside lock — blocks all callers!
    if err != nil { return Value{}, err }
    s.cache[key] = v
    return v, nil
}

// ✓ GOOD — singleflight + cache without holding lock during I/O
func (s *Store) GetOrFetch(key string) (Value, error) {
    if v, ok := s.getFromCache(key); ok { return v, nil }
    v, err, _ := s.sfg.Do(key, func() (any, error) { return s.db.Query(key) })
    if err != nil { return Value{}, err }
    s.setCache(key, v.(Value))
    return v.(Value), nil
}
func (s *Store) getFromCache(key string) (Value, bool) {
    s.mu.RLock(); defer s.mu.RUnlock()
    v, ok := s.cache[key]; return v, ok
}
```

---

## Chapter 17: Smells & Heuristics — Complete Reference

### Naming Smells (N1–N7)
```
N1: Choose descriptive names — names reveal intent
N2: Choose names at appropriate level of abstraction — not implementation details
N3: Use standard nomenclature where possible — design pattern names, domain terms
N4: Unambiguous names — no dual-meaning words, no confusing similarities
N5: Use long names for long scopes — short names for tiny scopes only
N6: Avoid encodings — no type prefixes, no Hungarian notation
N7: Names should describe side-effects — getOrCreate not just get
```

### Function Smells (F1–F4)
```
F1: Too many arguments — 0 ideal, 1-2 OK, 3+ extract to struct
F2: Output arguments — return instead of mutating parameter
F3: Flag arguments — split into two functions
F4: Dead function — delete it (git remembers)
```

### Comment Smells (C1–C5)
```
C1: Inappropriate information — implementation history belongs in git
C2: Obsolete comment — update or delete; stale comments lie
C3: Redundant comment — comment that says what the code says
C4: Poorly written comment — if needed, write it well; grammar matters
C5: Commented-out code — DELETE IT
```

### General Smells (G1–G36) — Complete
```
G1:  Multiple languages in one source file — one language per file
G2:  Obvious behavior not implemented — least surprise: do what name says
G3:  Incorrect behavior at boundaries — test every boundary condition
G4:  Overridden safeties — never turn off -race; never ignore go vet
G5:  Duplication — every duplication is a missed abstraction
G6:  Code at wrong level of abstraction — high-level/low-level must not mix
G7:  Base classes depending on derivatives — base never imports derived
G8:  Too much information — small interfaces, tight coupling only to what's needed
G9:  Dead code — delete unreachable code immediately
G10: Vertical separation — declare variables near first use
G11: Inconsistency — one word one concept; consistent naming and structure
G12: Clutter — remove all things with no purpose (empty constructors, unused vars)
G13: Artificial coupling — don't couple things that serve different purposes
G14: Feature envy — method that uses other class more than its own → move it
G15: Selector arguments — flag arguments smell; split into named functions
G16: Obscured intent — prefer expressive code over compact code
G17: Misplaced responsibility — code where it will be read/found naturally
G18: Inappropriate static — if function uses instance data → not static
G19: Use explanatory variables — extract complex expressions into named vars
G20: Function names should say what they do — if comment needed → rename
G21: Understand the algorithm — understand before coding; messy code = not understood
G22: Make logical dependencies physical — explicitly pass what you depend on
G23: Prefer polymorphism to if/else or switch — OO dispatch over conditionals
G24: Follow standard conventions — gofmt, godoc, test file naming
G25: Replace magic numbers with named constants — no bare literals
G26: Be precise — if you mean int, say int; if you mean float64, say float64
G27: Structure over convention — conventions rely on discipline; structure enforces
G28: Encapsulate conditionals — extract complex boolean to named function
G29: Avoid negative conditionals — !isNotActive → isActive
G30: Functions should do one thing — if multiple sections → multiple functions
G31: Hidden temporal couplings — make ordering explicit via function signatures
G32: Don't be arbitrary — if structure seems arbitrary, explain it or change it
G33: Encapsulate boundary conditions — off-by-one error hotspot; extract to var
G34: Functions should descend only one level of abstraction
G35: Keep configurable data at high levels — don't bury defaults deep in code
G36: Avoid transitive navigation — a.b().c() violates Demeter; ask direct friends
```

### Test Smells (T1–T9)
```
T1: Insufficient tests — test everything that can break
T2: Use a coverage tool — measure; 80%+ on business logic
T3: Don't skip trivial tests — trivial tests document behavior
T4: An ignored test is a question about ambiguity — clarify or delete
T5: Test boundary conditions — off-by-one, empty, null, overflow
T6: Exhaustively test near bugs — if one bug found, look for siblings
T7: Patterns of failure are revealing — sorted failures reveal structure
T8: Test coverage patterns reveal — uncovered lines often mean bad design
T9: Tests should be fast — slow tests don't get run; split into fast/slow suites
```

---

## Post-Flight Self-Audit Checklist

```
NAMING:
  □ Every name passes: "what is this? why does it exist? how is it used?"
  □ No abbreviations except universally understood (ctx, err, id, i)
  □ One word per concept — consistent across entire codebase
  □ No encoding, no noise words, no gratuitous context

FUNCTIONS:
  □ Every function ≤ 30 lines; most < 20
  □ Every function does ONE thing at ONE level of abstraction
  □ Step-Down Rule: public → protected → private; caller above callee
  □ Arguments: 0-2 preferred; 3+ extracted to struct
  □ No flag arguments; no output arguments; no nil returns (use error)
  □ No duplication anywhere; every repeat is a missed abstraction

COMMENTS:
  □ Zero redundant/explanatory comments — code explains itself
  □ Zero commented-out code — delete it
  □ Zero TODO older than one sprint without ticket
  □ All exported symbols have meaningful godoc

ERROR HANDLING:
  □ All errors wrapped with: fmt.Errorf("ReceiverType.Method(param): %w", err)
  □ No errors ignored (no _ for error)
  □ Nil never returned where zero value or ErrNotFound is correct
  □ Error taxonomy: sentinel for identity, typed for context

TESTS:
  □ Every exported function has table-driven tests
  □ t.Parallel() on all parallelizable tests
  □ One concept per test
  □ Tests are readable by non-author
  □ go test -race passes

CONCURRENCY:
  □ goroutines exit on ctx.Done()
  □ defer cancel() after every WithTimeout/WithCancel
  □ Mutex adjacent to guarded data, documented
  □ No I/O inside locked sections

GO IDIOMS:
  □ gofmt / goimports clean
  □ Interfaces defined at consumer site, small, implicit
  □ Early returns (guard clauses) over deep nesting
  □ go vet / golangci-lint zero warnings
```
