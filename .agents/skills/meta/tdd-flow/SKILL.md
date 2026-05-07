---
name: meta-tdd-flow
description: >
  Test-Driven Development workflow for Go: Red-Green-Refactor cycle, writing tests before
  implementation, interface-first design, test as specification, and TDD for different layers
  (domain, application, adapters). Load when asked to "TDD", "write tests first", or when
  building domain logic where correctness is critical. Cross-references: testing/SKILL.md.
---

# TDD Flow for Go — Red → Green → Refactor

## 1. The Cycle

```
RED:    Write a failing test that specifies ONE behavior
GREEN:  Write the MINIMUM code to make it pass (no gold-plating)
REFACTOR: Clean up code + tests while keeping green
REPEAT: Add next behavior
```

---

## 2. TDD for Domain Logic

```go
// STEP 1 — RED: write the test first (it won't compile yet)
func TestOrder_Confirm_WhenPending_Succeeds(t *testing.T) {
    t.Parallel()
    // Arrange
    o, err := order.NewOrder(
        order.CustomerID("cust-1"),
        []order.LineItem{testLineItem(t)},
    )
    require.NoError(t, err)
    assert.Equal(t, order.StatusPending, o.Status())

    // Act
    err = o.Confirm()

    // Assert
    require.NoError(t, err)
    assert.Equal(t, order.StatusConfirmed, o.Status())
    assert.Len(t, o.PopEvents(), 1)
    assert.IsType(t, order.OrderConfirmed{}, o.PopEvents()[0]) // BUG: PopEvents drains — fix in impl
}

// STEP 2 — GREEN: write minimum Order + Confirm to compile + pass
// STEP 3 — REFACTOR: improve naming, extract helpers, clean test

// Next RED: invalid transition
func TestOrder_Confirm_WhenAlreadyConfirmed_Fails(t *testing.T) {
    t.Parallel()
    o, _ := order.NewOrder(order.CustomerID("cust-1"), []order.LineItem{testLineItem(t)})
    _ = o.Confirm()  // first confirm

    err := o.Confirm()  // second confirm

    require.Error(t, err)
    assert.ErrorIs(t, err, order.ErrInvalidTransition)
}
```

---

## 3. TDD for Use Cases (Application Layer)

```go
// Design the interface FIRST via the test — before writing implementation
func TestCreateOrderHandler_ValidInput_CreatesAndPublishes(t *testing.T) {
    t.Parallel()

    // Arrange: define what collaborators must do
    repo := mocks.NewMockRepository(t)
    pub  := mocks.NewMockEventPublisher(t)

    repo.EXPECT().
        Save(mock.Anything, mock.MatchedBy(func(o *order.Order) bool {
            return o.CustomerID() == "cust-abc"
        })).
        Return(nil).Once()

    pub.EXPECT().
        Publish(mock.Anything, mock.Anything).
        Return(nil).Once()

    h := NewCreateOrderHandler(repo, pub, validator.New())

    // Act
    result, err := h.Handle(context.Background(), CreateOrderCommand{
        CustomerID: "cust-abc",
        Items: []ItemInput{{ProductID: "prod-1", Quantity: 2}},
    })

    // Assert
    require.NoError(t, err)
    assert.NotEmpty(t, result.ID)
    assert.Equal(t, "pending", result.Status)
}

// TDD reveals interface design: what do we need from collaborators?
// → Repository.Save(ctx, *order.Order) error
// → EventPublisher.Publish(ctx, DomainEvent) error
// Now implement them.
```

---

## 4. TDD for HTTP Handlers

```go
// Start with the HTTP contract — before implementing the handler
func TestOrderHandler_POST_ValidBody_Returns201(t *testing.T) {
    t.Parallel()

    // Arrange: mock the use case (handler should not know about DB)
    svc := mocks.NewMockCreateOrderUseCase(t)
    svc.EXPECT().Handle(mock.Anything, mock.Anything).
        Return(&OrderResponse{ID: "order-123", Status: "pending"}, nil).Once()

    r := chi.NewRouter()
    r.Post("/orders", NewOrderHandler(svc).Create)

    body := `{"customer_id":"cust-1","items":[{"product_id":"prod-1","quantity":2}]}`
    req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()

    // Act
    r.ServeHTTP(w, req)

    // Assert HTTP contract
    assert.Equal(t, http.StatusCreated, w.Code)
    assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

    var resp Response[OrderView]
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
    assert.Equal(t, "order-123", resp.Data.ID)
}

func TestOrderHandler_POST_InvalidBody_Returns422(t *testing.T) { /* ... */ }
func TestOrderHandler_POST_MissingContentType_Returns415(t *testing.T) { /* ... */ }
func TestOrderHandler_POST_ServiceError_Returns500(t *testing.T) { /* ... */ }
```

---

## 5. TDD for Repositories (Integration)

```go
// Test the DB contract — what does the domain require?
func TestOrderRepository_Save_FindByID_RoundTrip(t *testing.T) {
    if testing.Short() { t.Skip() }
    t.Parallel()

    pool := testutil.MustSetupDB(t)  // testcontainers
    repo := postgres.NewOrderRepository(pool)
    ctx := context.Background()

    // Arrange
    original, _ := order.NewOrder(order.CustomerID("cust-test"), testItems())

    // Act: save
    require.NoError(t, repo.Save(ctx, original))

    // Act: find
    found, err := repo.FindByID(ctx, original.ID())

    // Assert domain contract, not DB internals
    require.NoError(t, err)
    assert.Equal(t, original.ID(), found.ID())
    assert.Equal(t, original.Status(), found.Status())
    assert.Equal(t, original.Total(), found.Total())
    assert.Equal(t, original.Version(), found.Version())
}

func TestOrderRepository_Save_VersionConflict_ReturnsErrConflict(t *testing.T) {
    if testing.Short() { t.Skip() }
    t.Parallel()

    pool := testutil.MustSetupDB(t)
    repo := postgres.NewOrderRepository(pool)
    ctx := context.Background()

    o, _ := order.NewOrder(order.CustomerID("c"), testItems())
    require.NoError(t, repo.Save(ctx, o))

    // Simulate concurrent update (advance version externally)
    _, _ = pool.Exec(ctx, `UPDATE orders SET version = version + 1 WHERE id = $1`, o.ID())

    err := repo.Save(ctx, o)  // stale version
    assert.ErrorIs(t, err, domain.ErrConflict)
}
```

---

## 6. TDD Discipline Rules

```
1. Never write production code without a failing test first
2. Write the simplest test that could fail (one behavior per test)
3. Write the minimum code to make it green — resist over-engineering
4. Refactor only when green — never refactor on red
5. Test names: TestSubject_Scenario_ExpectedOutcome
6. One assertion concept per test (multiple assert.X for same concept is fine)
7. Tests are documentation — they specify behavior, not implementation
8. If you need a mock for a type that doesn't exist → you've discovered an interface
9. Slow tests (DB, network) are integration tests — separate from unit tests
10. 100% test coverage is NOT the goal — testing ALL behaviors IS the goal
```

---

## TDD Checklist

- [ ] Test written BEFORE implementation (check git history)
- [ ] Test fails for the right reason (not compilation error — actual assertion failure)
- [ ] Implementation is minimal — passes tests without extra code
- [ ] Refactor step: no new tests needed, all existing still pass
- [ ] Domain tests: pure unit tests, zero mocks for internal domain logic
- [ ] App layer tests: mock collaborators (repo, publisher) via mockery
- [ ] Handler tests: mock use cases, test HTTP contract (status, body, headers)
- [ ] Repo tests: real DB via testcontainers, test domain error mapping
