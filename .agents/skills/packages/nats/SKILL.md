---
name: pkg-nats
description: >
  Exhaustive reference for nats-io/nats.go: connection setup, core publish/subscribe,
  request/reply, JetStream (persistent messaging), key-value store, object store,
  queue groups, wildcard subjects, and error handling. Cross-references: event-driven/SKILL.md.
---

# Package: nats-io/nats.go — Complete Reference

## Import
```go
import (
    "github.com/nats-io/nats.go"
    "github.com/nats-io/nats.go/jetstream"
)
```

## 1. Connection Setup

```go
func NewNATSConn(servers []string, opts ...nats.Option) (*nats.Conn, error) {
    defaults := []nats.Option{
        nats.MaxReconnects(-1),                    // reconnect forever
        nats.ReconnectWait(2 * time.Second),
        nats.ReconnectJitter(500*time.Millisecond, 2*time.Second),
        nats.Timeout(5 * time.Second),
        nats.PingInterval(20 * time.Second),
        nats.MaxPingsOutstanding(3),
        nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
            slog.Error("nats error", "sub", sub.Subject, "err", err)
        }),
        nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
            slog.Warn("nats disconnected", "err", err)
        }),
        nats.ReconnectHandler(func(_ *nats.Conn) {
            slog.Info("nats reconnected")
        }),
        nats.ClosedHandler(func(_ *nats.Conn) {
            slog.Info("nats connection closed")
        }),
    }

    nc, err := nats.Connect(strings.Join(servers, ","), append(defaults, opts...)...)
    if err != nil { return nil, fmt.Errorf("nats.Connect: %w", err) }
    return nc, nil
}
```

## 2. Core Pub/Sub

```go
// Publish (fire and forget)
err := nc.Publish("orders.created", payload)

// Subscribe
sub, err := nc.Subscribe("orders.*", func(msg *nats.Msg) {
    // msg.Subject: "orders.created", "orders.confirmed" etc.
    // msg.Data: message payload
    // NEVER block in callback — dispatch to goroutine
    go handleOrderEvent(context.Background(), msg)
})
defer sub.Unsubscribe()

// Queue group (load-balanced — only one subscriber in group receives each message)
sub, err = nc.QueueSubscribe("orders.created", "order-service", func(msg *nats.Msg) {
    go handleOrderCreated(context.Background(), msg)
})

// Request/Reply (synchronous pattern)
resp, err := nc.Request("inventory.check", requestPayload, 5*time.Second)
if err != nil { return nil, fmt.Errorf("inventory check timeout: %w", err) }

// Reply side
nc.Subscribe("inventory.check", func(msg *nats.Msg) {
    result := checkInventory(msg.Data)
    msg.Respond(result)  // send reply to msg.Reply subject
})
```

## 3. JetStream (Persistent Messaging)

```go
js, err := jetstream.New(nc)
if err != nil { return nil, fmt.Errorf("jetstream.New: %w", err) }

// Create stream (idempotent — safe to call on startup)
stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
    Name:       "ORDERS",
    Subjects:   []string{"orders.>"},  // ">" = multi-level wildcard
    Retention:  jetstream.LimitsPolicy,
    MaxAge:     7 * 24 * time.Hour,
    MaxBytes:   1 << 30,    // 1 GB
    MaxMsgs:    -1,          // unlimited count
    Replicas:   3,           // HA
    Storage:    jetstream.FileStorage,
    Discard:    jetstream.DiscardOld,
})

// Publish to JetStream (ack'd — guaranteed delivery)
ack, err := js.Publish(ctx, "orders.created", payload)
if err != nil { return nil, fmt.Errorf("js.Publish: %w", err) }
// ack.Sequence — stream sequence number

// Create consumer
cons, err := js.CreateOrUpdateConsumer(ctx, "ORDERS", jetstream.ConsumerConfig{
    Name:           "order-service",
    Durable:        "order-service",   // durable = survives restarts
    AckPolicy:      jetstream.AckExplicitPolicy,
    AckWait:        30 * time.Second,
    MaxDeliver:     5,                 // max retries before moving to DLQ
    FilterSubject:  "orders.created",
    DeliverPolicy:  jetstream.DeliverAllPolicy,
})

// Consume messages
cc, err := cons.Consume(func(msg jetstream.Msg) {
    ctx := context.Background()

    if err := handleMessage(ctx, msg); err != nil {
        if isRetryable(err) && msg.Metadata().NumDelivered < 5 {
            msg.Nak()  // nack — will be redelivered after AckWait
            return
        }
        msg.Term()  // terminal failure — move to DLQ stream
        return
    }
    msg.Ack()  // acknowledge — message removed from pending
})
defer cc.Stop()
```

## 4. Key-Value Store (JetStream)

```go
// KV store backed by JetStream
kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
    Bucket:   "user-sessions",
    TTL:      24 * time.Hour,
    Replicas: 3,
})

// Put
rev, err := kv.Put(ctx, "session:"+sessionID, sessionData)

// Get
entry, err := kv.Get(ctx, "session:"+sessionID)
if errors.Is(err, jetstream.ErrKeyNotFound) { return nil, ErrSessionNotFound }
data := entry.Value()

// Update with revision check (optimistic lock)
_, err = kv.Update(ctx, "session:"+sessionID, newData, entry.Revision())
if errors.Is(err, jetstream.ErrKeyWrongLastIndex) { return ErrConflict }

// Delete
err = kv.Delete(ctx, "session:"+sessionID)

// Watch for changes (reactive)
watcher, _ := kv.Watch(ctx, "session.*")
defer watcher.Stop()
for entry := range watcher.Updates() {
    if entry == nil { break } // initial values delivered; nil signals end
    handleSessionChange(entry)
}
```

## 5. Subject Naming Conventions

```
Hierarchy: service.entity.action
Orders:    orders.created | orders.confirmed | orders.cancelled
Users:     users.registered | users.updated | users.deleted
Wildcards:
  * (single token): orders.* matches orders.created, orders.confirmed
  > (multi-level):  orders.> matches orders.created, orders.v2.created

Request/Reply subjects (ephemeral):
  _INBOX.xxxxx  — generated by nc.NewInbox()
```

## NATS Checklist
- [ ] `MaxReconnects(-1)` — reconnect indefinitely with backoff
- [ ] Error/disconnect/reconnect handlers logged with slog
- [ ] Queue groups used for load-balanced consumers (not plain Subscribe)
- [ ] JetStream consumers are durable — survive restarts
- [ ] `AckPolicy: AckExplicit` — never use `AckNone` for important messages
- [ ] `msg.Ack()` called on success, `msg.Nak()` on retryable error, `msg.Term()` on fatal
- [ ] `MaxDeliver` set on consumers to prevent infinite retry loops
- [ ] KV revision used for optimistic locking on updates
- [ ] `nc.Drain()` on shutdown — flushes pending messages before close
- [ ] `defer cc.Stop()` / `defer sub.Unsubscribe()` on all consumers
