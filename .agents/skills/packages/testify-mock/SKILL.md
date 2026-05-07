---
name: pkg-testify-mock
description: >
  Exhaustive reference for testify/mock and testify/suite: mock object setup, EXPECT() API,
  argument matchers, call ordering, mock verification, suite lifecycle hooks, and anti-patterns.
  Cross-references: testing/SKILL.md, meta/tdd-flow/SKILL.md.
---

# Package: testify/mock + testify/suite — Complete Reference

## Import
```go
import (
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "github.com/stretchr/testify/mock"
    "github.com/stretchr/testify/suite"
)
```

## 1. Mock Generation (prefer mockery)

```go
// Generate: mockery --name=Repository --with-expecter --output=mocks
// Hand-written pattern (for small interfaces):

type MockOrderRepository struct {
    mock.Mock
}

func (m *MockOrderRepository) FindByID(ctx context.Context, id order.ID) (*order.Order, error) {
    args := m.Called(ctx, id)
    if args.Get(0) == nil { return nil, args.Error(1) }  // handle nil *Order
    return args.Get(0).(*order.Order), args.Error(1)
}

func (m *MockOrderRepository) Save(ctx context.Context, o *order.Order) error {
    return m.Called(ctx, o).Error(0)
}
```

## 2. EXPECT() Fluent API (mockery with-expecter)

```go
func TestCreateOrder(t *testing.T) {
    t.Parallel()

    // NewMock(t) registers t.Cleanup(mock.AssertExpectations) automatically
    repo := mocks.NewMockOrderRepository(t)
    publisher := mocks.NewMockEventPublisher(t)

    // Set expectations using EXPECT() fluent API
    repo.EXPECT().
        Save(
            mock.Anything,  // ctx — any context
            mock.MatchedBy(func(o *order.Order) bool {
                return o.CustomerID() == "cust-1" && o.Status() == order.StatusPending
            }),
        ).
        Return(nil).   // error = nil (success)
        Once()         // must be called exactly once

    publisher.EXPECT().
        Publish(mock.Anything, mock.AnythingOfType("order.OrderCreated")).
        Return(nil).
        Once()

    h := NewCreateOrderHandler(repo, publisher, validator.New())
    result, err := h.Handle(context.Background(), CreateOrderCommand{
        CustomerID: "cust-1",
        Items:      testItems(),
    })

    require.NoError(t, err)
    assert.Equal(t, "cust-1", result.CustomerID)
    // AssertExpectations called automatically via t.Cleanup
}
```

## 3. Argument Matchers

```go
mock.Anything                          // matches any value
mock.AnythingOfType("string")          // matches exact type name
mock.AnythingOfTypeArgument("*order.Order") // pointer type
mock.MatchedBy(func(v T) bool { ... }) // custom predicate
mock.IsType(&order.Order{})            // matches same type

// Context matcher — match any non-nil context
mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil })

// Error matcher
mock.MatchedBy(func(err error) bool { return errors.Is(err, domain.ErrNotFound) })
```

## 4. Return Values

```go
// Return static values
m.EXPECT().FindByID(mock.Anything, id).Return(user, nil)

// Return error
m.EXPECT().Save(mock.Anything, mock.Anything).Return(domain.ErrConflict)

// Return function (dynamic return based on input)
m.EXPECT().FindByID(mock.Anything, mock.Anything).
    RunAndReturn(func(ctx context.Context, id string) (*User, error) {
        if id == "missing" { return nil, domain.ErrNotFound }
        return &User{ID: id}, nil
    })

// Run: side effect WITHOUT changing return values
m.EXPECT().Save(mock.Anything, mock.Anything).
    Run(func(ctx context.Context, u *User) {
        // capture the saved user for assertion
        capturedUser = u
    }).
    Return(nil)
```

## 5. Call Count Expectations

```go
.Once()           // exactly once
.Twice()          // exactly twice
.Times(n)         // exactly n times
.Maybe()          // zero or more (optional call — won't fail if not called)

// Without count: default is "at least once if set up"
// Use .Maybe() for optional calls that may or may not happen
```

## 6. Call Ordering

```go
// Assert calls happen in sequence
call1 := m.EXPECT().Begin(mock.Anything).Return(tx, nil).Once()
call2 := m.EXPECT().Save(mock.Anything, mock.Anything).Return(nil).Once().NotBefore(call1)
call3 := m.EXPECT().Commit(mock.Anything).Return(nil).Once().NotBefore(call2)
```

## 7. testify/suite

```go
// Suite: share expensive setup across related tests (e.g., DB connection)
type OrderRepositoryTestSuite struct {
    suite.Suite
    pool *pgxpool.Pool
    repo *postgres.OrderRepository
    ctx  context.Context
}

// SetupSuite: runs ONCE before all tests in the suite
func (s *OrderRepositoryTestSuite) SetupSuite() {
    s.ctx = context.Background()
    s.pool = testutil.MustSetupDB(s.T())  // testcontainers
    s.repo = postgres.NewOrderRepository(s.pool)
}

// TearDownSuite: runs ONCE after all tests
func (s *OrderRepositoryTestSuite) TearDownSuite() {
    s.pool.Close()
}

// SetupTest: runs before EACH test (clean state)
func (s *OrderRepositoryTestSuite) SetupTest() {
    _, _ = s.pool.Exec(s.ctx, `TRUNCATE orders CASCADE`)
}

// Tests use s.Require() and s.Assert() (methods on suite.Suite)
func (s *OrderRepositoryTestSuite) TestSave_ValidOrder_Succeeds() {
    o, err := order.NewOrder(order.CustomerID("cust-1"), testItems())
    s.Require().NoError(err)

    err = s.repo.Save(s.ctx, o)
    s.Assert().NoError(err)
}

func (s *OrderRepositoryTestSuite) TestFindByID_NotFound_ReturnsErrNotFound() {
    _, err := s.repo.FindByID(s.ctx, order.NewID())
    s.Assert().ErrorIs(err, domain.ErrNotFound)
}

// Register suite with testing framework
func TestOrderRepositoryTestSuite(t *testing.T) {
    suite.Run(t, new(OrderRepositoryTestSuite))
}
```

## 8. assert vs require

```go
// assert: test continues after failure (non-fatal)
assert.Equal(t, expected, actual)
assert.NoError(t, err)
assert.True(t, condition)
assert.Len(t, slice, 3)
assert.Contains(t, str, "substring")
assert.ErrorIs(t, err, target)
assert.IsType(t, &User{}, actual)
assert.Nil(t, pointer)
assert.NotNil(t, pointer)

// require: test STOPS after failure (fatal — use when later assertions depend on this one)
require.NoError(t, err)    // stop if err != nil — don't run assertions on nil result
require.NotNil(t, result)  // stop if nil — avoid nil pointer in subsequent assertions
require.Len(t, items, 3)   // stop if wrong length — subsequent index access would panic
```

## testify Checklist
- [ ] `mocks.NewMockX(t)` used — auto-registers `AssertExpectations` via `t.Cleanup`
- [ ] Never call `mock.AssertExpectations(t)` manually when using `NewMockX(t)`
- [ ] `mock.Anything` for `context.Context` parameters (don't assert specific ctx values)
- [ ] `mock.MatchedBy` for structural matching of complex arguments
- [ ] `.Maybe()` for optional calls — prevents false failures
- [ ] `require.NoError` before accessing return values that may be nil
- [ ] Suite used when tests share expensive setup (DB, containers)
- [ ] `SetupTest` (not `SetupSuite`) for per-test state reset
