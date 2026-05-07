---
name: golang-event-driven
description: >
  Go event-driven architecture: outbox pattern, saga pattern (choreography and orchestration),
  event sourcing, at-least-once delivery guarantees, idempotent consumers, dead-letter queues,
  event schema evolution, CloudEvents spec, and event-driven microservice coordination.
  Use when building any event-driven service, Kafka consumer, message queue integration,
  or saga-based distributed transaction. Always combine with architecture/SKILL.md.
---

# Go Event-Driven Architecture

## 1. Outbox Pattern (Guaranteed Delivery)

```
Problem: Write to DB + publish event = two operations → can fail between them.
Solution: Write event TO DB in same transaction, then relay to message broker.

  ┌──────────┐  same TX  ┌──────────────┐
  │  Orders  │◄──────────│  Outbox Tbl  │
  └──────────┘           └──────┬───────┘
                                │ relay (async)
                          ┌─────▼──────┐
                          │   Kafka    │
                          └────────────┘
```

```go
// 1. Outbox table
// CREATE TABLE outbox (
//   id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
//   event_type TEXT NOT NULL,
//   aggregate_id TEXT NOT NULL,
//   aggregate_type TEXT NOT NULL,
//   payload JSONB NOT NULL,
//   created_at TIMESTAMPTZ DEFAULT NOW(),
//   processed_at TIMESTAMPTZ,
//   attempts INT DEFAULT 0,
//   last_error TEXT
// );
// CREATE INDEX outbox_unprocessed ON outbox (created_at) WHERE processed_at IS NULL;

type OutboxEvent struct {
    ID            uuid.UUID
    EventType     string
    AggregateID   string
    AggregateType string
    Payload       json.RawMessage
    CreatedAt     time.Time
}

// 2. Write to outbox IN THE SAME TRANSACTION as the domain write
func (r *OrderRepo) SaveWithEvents(ctx context.Context, tx pgx.Tx, o *order.Order, events []domain.Event) error {
    // Domain write
    if _, err := tx.Exec(ctx, `INSERT INTO orders ...`, orderArgs(o)...); err != nil {
        return fmt.Errorf("SaveWithEvents.insert: %w", err)
    }

    // Outbox writes — same transaction
    for _, evt := range events {
        payload, err := json.Marshal(evt)
        if err != nil { return fmt.Errorf("SaveWithEvents.marshal(%s): %w", evt.EventName(), err) }

        if _, err := tx.Exec(ctx, `
            INSERT INTO outbox (event_type, aggregate_id, aggregate_type, payload)
            VALUES ($1, $2, $3, $4)`,
            evt.EventName(), o.ID().String(), "order", payload,
        ); err != nil {
            return fmt.Errorf("SaveWithEvents.outbox: %w", err)
        }
    }
    return nil
}

// 3. Outbox relay — background worker polls and publishes
type OutboxRelay struct {
    db       *pgxpool.Pool
    producer MessageProducer
    interval time.Duration
    batchSz  int
    logger   *slog.Logger
}

func (r *OutboxRelay) Run(ctx context.Context) error {
    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done(): return nil
        case <-ticker.C:
            if err := r.processOnce(ctx); err != nil {
                r.logger.ErrorContext(ctx, "outbox relay error", "err", err)
            }
        }
    }
}

func (r *OutboxRelay) processOnce(ctx context.Context) error {
    // SELECT FOR UPDATE SKIP LOCKED prevents concurrent relay workers from double-processing
    rows, err := r.db.Query(ctx, `
        SELECT id, event_type, aggregate_id, aggregate_type, payload
        FROM outbox
        WHERE processed_at IS NULL AND attempts < 5
        ORDER BY created_at
        LIMIT $1
        FOR UPDATE SKIP LOCKED`, r.batchSz)
    if err != nil { return fmt.Errorf("OutboxRelay.query: %w", err) }
    defer rows.Close()

    var events []OutboxEvent
    for rows.Next() {
        var e OutboxEvent
        if err := rows.Scan(&e.ID, &e.EventType, &e.AggregateID, &e.AggregateType, &e.Payload); err != nil {
            return err
        }
        events = append(events, e)
    }
    if err := rows.Err(); err != nil { return err }
    rows.Close()

    for _, e := range events {
        pubErr := r.producer.Publish(ctx, e.EventType, e.AggregateID, e.Payload)

        if pubErr != nil {
            _, _ = r.db.Exec(ctx, `
                UPDATE outbox SET attempts = attempts + 1, last_error = $1
                WHERE id = $2`, pubErr.Error(), e.ID)
        } else {
            _, _ = r.db.Exec(ctx, `
                UPDATE outbox SET processed_at = NOW() WHERE id = $1`, e.ID)
        }
    }
    return nil
}
```

---

## 2. Idempotent Consumer

```go
// Every consumer MUST be idempotent: handle the same message multiple times safely.
// Use a processed_events table keyed by (event_id, consumer_group).

type IdempotentConsumer struct {
    db      *pgxpool.Pool
    handler EventHandler
    group   string
}

func (c *IdempotentConsumer) Handle(ctx context.Context, msg Message) error {
    // Check if already processed (idempotency key = msg.ID)
    var alreadyDone bool
    err := c.db.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM processed_events
            WHERE event_id = $1 AND consumer_group = $2
        )`, msg.ID, c.group).Scan(&alreadyDone)
    if err != nil { return fmt.Errorf("IdempotentConsumer.check: %w", err) }
    if alreadyDone {
        slog.InfoContext(ctx, "duplicate event skipped",
            "event_id", msg.ID, "group", c.group)
        return nil // success — already handled
    }

    // Process in transaction — mark done AND apply effect atomically
    tx, err := c.db.Begin(ctx)
    if err != nil { return err }
    defer tx.Rollback(ctx)

    if err := c.handler.Handle(ctx, tx, msg); err != nil {
        return fmt.Errorf("IdempotentConsumer.handle: %w", err)
    }

    if _, err := tx.Exec(ctx, `
        INSERT INTO processed_events (event_id, consumer_group, processed_at)
        VALUES ($1, $2, NOW())
        ON CONFLICT DO NOTHING`, msg.ID, c.group); err != nil {
        return fmt.Errorf("IdempotentConsumer.markDone: %w", err)
    }

    return tx.Commit(ctx)
}
```

---

## 3. Saga Pattern — Choreography

```go
// Choreography: each service reacts to events from other services.
// No central coordinator. Loose coupling. Complex to trace.

// Order saga: Order.Created → Inventory.Reserved → Payment.Charged → Order.Confirmed

// InventoryService listens to order.created
type InventoryOrderCreatedHandler struct {
    inventory InventoryService
    publisher EventPublisher
}

func (h *InventoryOrderCreatedHandler) Handle(ctx context.Context, tx pgx.Tx, msg Message) error {
    var evt OrderCreatedEvent
    if err := json.Unmarshal(msg.Payload, &evt); err != nil { return err }

    // Try to reserve stock
    reservation, err := h.inventory.Reserve(ctx, tx, evt.Items)
    if err != nil {
        // Compensating event: tell order service reservation failed
        return h.publisher.Publish(ctx, "inventory.reservation_failed", evt.OrderID, ReservationFailedEvent{
            OrderID: evt.OrderID,
            Reason:  err.Error(),
        })
    }

    // Success: emit event for next saga step
    return h.publisher.Publish(ctx, "inventory.reserved", evt.OrderID, InventoryReservedEvent{
        OrderID:       evt.OrderID,
        ReservationID: reservation.ID,
    })
}
```

---

## 4. Saga Pattern — Orchestration

```go
// Orchestration: one saga coordinator drives all steps.
// Easier to trace, single point of failure, but explicit control.

type OrderSagaState struct {
    OrderID       string
    Step          SagaStep
    ReservationID string
    PaymentID     string
    FailedAt      *SagaStep
    CompensatedAt *time.Time
}

type SagaStep string
const (
    SagaStepReserveInventory SagaStep = "reserve_inventory"
    SagaStepChargePayment    SagaStep = "charge_payment"
    SagaStepConfirmOrder     SagaStep = "confirm_order"
    SagaStepCompleted        SagaStep = "completed"
    SagaStepFailed           SagaStep = "failed"
)

type OrderSagaOrchestrator struct {
    sagas     SagaRepository
    inventory InventoryClient
    payment   PaymentClient
    orders    OrderClient
    logger    *slog.Logger
}

func (o *OrderSagaOrchestrator) Execute(ctx context.Context, orderID string) error {
    saga := &OrderSagaState{OrderID: orderID, Step: SagaStepReserveInventory}
    if err := o.sagas.Save(ctx, saga); err != nil { return err }

    // Step 1: Reserve inventory
    res, err := o.inventory.Reserve(ctx, orderID)
    if err != nil {
        saga.Step, saga.FailedAt = SagaStepFailed, ptr(SagaStepReserveInventory)
        o.sagas.Save(ctx, saga)
        return fmt.Errorf("saga.ReserveInventory: %w", err)
        // No compensation needed — nothing done yet
    }
    saga.ReservationID = res.ID
    saga.Step = SagaStepChargePayment
    o.sagas.Save(ctx, saga)

    // Step 2: Charge payment
    pay, err := o.payment.Charge(ctx, orderID)
    if err != nil {
        saga.Step, saga.FailedAt = SagaStepFailed, ptr(SagaStepChargePayment)
        o.sagas.Save(ctx, saga)
        // Compensate: release inventory reservation
        if cerr := o.inventory.Release(ctx, saga.ReservationID); cerr != nil {
            o.logger.ErrorContext(ctx, "saga compensation failed", "err", cerr)
        }
        return fmt.Errorf("saga.ChargePayment: %w", err)
    }
    saga.PaymentID = pay.ID
    saga.Step = SagaStepConfirmOrder
    o.sagas.Save(ctx, saga)

    // Step 3: Confirm order
    if err := o.orders.Confirm(ctx, orderID); err != nil {
        saga.Step = SagaStepFailed
        o.sagas.Save(ctx, saga)
        // Compensate: refund payment + release inventory
        _ = o.payment.Refund(ctx, saga.PaymentID)
        _ = o.inventory.Release(ctx, saga.ReservationID)
        return fmt.Errorf("saga.ConfirmOrder: %w", err)
    }

    saga.Step = SagaStepCompleted
    return o.sagas.Save(ctx, saga)
}
```

---

## 5. CloudEvents Schema

```go
// Use CloudEvents spec (CNCF standard) for portable event envelopes
type CloudEvent struct {
    SpecVersion     string          `json:"specversion"`           // "1.0"
    ID              string          `json:"id"`                    // uuid
    Source          string          `json:"source"`                // /myservice/orders
    Type            string          `json:"type"`                  // com.example.order.created
    Time            time.Time       `json:"time"`
    DataContentType string          `json:"datacontenttype"`       // application/json
    DataSchema      string          `json:"dataschema,omitempty"`  // schema URL
    Subject         string          `json:"subject,omitempty"`     // orderID
    Data            json.RawMessage `json:"data"`
}

func NewCloudEvent(source, eventType, subject string, data any) (*CloudEvent, error) {
    payload, err := json.Marshal(data)
    if err != nil { return nil, fmt.Errorf("NewCloudEvent.marshal: %w", err) }
    return &CloudEvent{
        SpecVersion: "1.0", ID: uuid.New().String(),
        Source: source, Type: eventType, Subject: subject,
        Time: time.Now().UTC(), DataContentType: "application/json",
        Data: payload,
    }, nil
}
```

---

## Event-Driven Checklist

- [ ] Outbox pattern used for all domain-event-to-broker publishing
- [ ] Outbox and domain writes in same database transaction
- [ ] Outbox relay uses `SELECT FOR UPDATE SKIP LOCKED` for safe parallel processing
- [ ] All consumers are idempotent (processed_events deduplication)
- [ ] Saga state persisted before each step and after each compensation
- [ ] Compensating transactions defined for every saga step that can fail mid-saga
- [ ] Dead-letter queue (DLQ) configured for messages that fail after max retries
- [ ] Events versioned (event schema evolution: additive only, never remove fields)
- [ ] CloudEvents spec used for portable event envelope
- [ ] Consumer lag monitored as key SLI
