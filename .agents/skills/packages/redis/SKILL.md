---
name: pkg-redis
description: >
  Exhaustive reference for github.com/redis/go-redis/v9: client setup (single, cluster, sentinel),
  basic operations, pipelining, transactions (MULTI/EXEC), Lua scripts, pub/sub, streams,
  distributed locks (Redlock), caching patterns, and error handling. Cross-references:
  database/SKILL.md, concurrency/SKILL.md.
---

# Package: redis/go-redis/v9 — Complete Reference

## Import
```go
import "github.com/redis/go-redis/v9"
```

## 1. Client Setup

```go
// Single node
func NewRedisClient(cfg RedisConfig) (*redis.Client, error) {
    rdb := redis.NewClient(&redis.Options{
        Addr:            cfg.Addr,      // "localhost:6379"
        Password:        cfg.Password,
        DB:              cfg.DB,        // 0-15
        MaxRetries:      3,
        MinRetryBackoff: 8 * time.Millisecond,
        MaxRetryBackoff: 512 * time.Millisecond,
        DialTimeout:     5 * time.Second,
        ReadTimeout:     3 * time.Second,
        WriteTimeout:    3 * time.Second,
        PoolSize:        10,
        MinIdleConns:    5,
        MaxIdleTime:     5 * time.Minute,
        ConnMaxLifetime: 30 * time.Minute,
    })

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := rdb.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("redis.Ping: %w", err)
    }
    return rdb, nil
}

// Cluster
rdb := redis.NewClusterClient(&redis.ClusterOptions{
    Addrs:    []string{"node1:6379", "node2:6379", "node3:6379"},
    Password: cfg.Password,
    // Same timeout/pool options as above
})

// Sentinel (HA)
rdb := redis.NewFailoverClient(&redis.FailoverOptions{
    MasterName:    "mymaster",
    SentinelAddrs: []string{"sentinel1:26379", "sentinel2:26379"},
    Password:      cfg.Password,
})
```

## 2. Basic Operations

```go
// SET with TTL (always use TTL — never store without expiry in caches)
err := rdb.Set(ctx, "user:"+id, serialized, 1*time.Hour).Err()

// GET with nil check
val, err := rdb.Get(ctx, "user:"+id).Result()
if errors.Is(err, redis.Nil) {
    return nil, ErrCacheMiss  // key does not exist
}
if err != nil { return nil, fmt.Errorf("redis.Get: %w", err) }

// SETNX (set if not exists) — for distributed locks
ok, err := rdb.SetNX(ctx, "lock:resource", token, 30*time.Second).Result()
if !ok { /* already locked */ }

// GETSET — atomic get+set
old, err := rdb.GetSet(ctx, "counter", "0").Result()

// DEL
deleted, err := rdb.Del(ctx, "key1", "key2").Result()

// EXISTS
n, err := rdb.Exists(ctx, "key1", "key2").Result()  // returns count of existing keys

// EXPIRE / TTL
rdb.Expire(ctx, "key", 10*time.Minute)
ttl, err := rdb.TTL(ctx, "key").Result()

// INCR / DECR
newVal, err := rdb.Incr(ctx, "page:views").Result()
rdb.IncrBy(ctx, "counter", 5)
rdb.DecrBy(ctx, "counter", 2)
```

## 3. JSON Caching Pattern

```go
type UserCache struct {
    rdb *redis.Client
    ttl time.Duration
}

func (c *UserCache) Get(ctx context.Context, id string) (*User, error) {
    key := "user:" + id
    data, err := c.rdb.Get(ctx, key).Bytes()
    if errors.Is(err, redis.Nil) { return nil, ErrCacheMiss }
    if err != nil { return nil, fmt.Errorf("UserCache.Get: %w", err) }

    var u User
    if err := json.Unmarshal(data, &u); err != nil {
        return nil, fmt.Errorf("UserCache.Get.unmarshal: %w", err)
    }
    return &u, nil
}

func (c *UserCache) Set(ctx context.Context, u *User) error {
    data, err := json.Marshal(u)
    if err != nil { return fmt.Errorf("UserCache.Set.marshal: %w", err) }
    return c.rdb.Set(ctx, "user:"+u.ID, data, c.ttl).Err()
}

func (c *UserCache) Delete(ctx context.Context, id string) error {
    return c.rdb.Del(ctx, "user:"+id).Err()
}
```

## 4. Pipelining (Batch Operations)

```go
// Pipeline: batch commands in one round trip (fire-and-forget)
pipe := rdb.Pipeline()
incr := pipe.Incr(ctx, "counter")
pipe.Expire(ctx, "counter", 1*time.Hour)
_, err := pipe.Exec(ctx)
if err != nil { return fmt.Errorf("pipeline.Exec: %w", err) }
fmt.Println(incr.Val())

// Pipelined (callback style)
_, err = rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
    for _, key := range keys {
        pipe.Get(ctx, key)
    }
    return nil
})
```

## 5. Transactions (MULTI/EXEC with WATCH)

```go
// Optimistic lock: WATCH + MULTI/EXEC
func TransferCredits(ctx context.Context, rdb *redis.Client, fromID, toID string, amount int64) error {
    fromKey := "credits:" + fromID
    toKey   := "credits:" + toID

    // Retry on WATCH conflict
    for i := 0; i < 3; i++ {
        err := rdb.Watch(ctx, func(tx *redis.Tx) error {
            // Check current balance inside WATCH
            bal, err := tx.Get(ctx, fromKey).Int64()
            if err != nil && !errors.Is(err, redis.Nil) { return err }
            if bal < amount { return ErrInsufficientCredits }

            // Execute atomically
            _, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
                pipe.DecrBy(ctx, fromKey, amount)
                pipe.IncrBy(ctx, toKey, amount)
                return nil
            })
            return err
        }, fromKey, toKey)

        if err == nil { return nil }
        if errors.Is(err, redis.TxFailedErr) { continue }  // WATCH conflict — retry
        return fmt.Errorf("TransferCredits: %w", err)
    }
    return fmt.Errorf("TransferCredits: too many retries")
}
```

## 6. Distributed Lock

```go
// Simple distributed lock (use redsync for production Redlock)
type DistributedLock struct {
    rdb   *redis.Client
    key   string
    token string
    ttl   time.Duration
}

func (l *DistributedLock) Acquire(ctx context.Context) (bool, error) {
    return l.rdb.SetNX(ctx, l.key, l.token, l.ttl).Result()
}

// Release: only release if we own it (Lua script for atomicity)
var releaseLockScript = redis.NewScript(`
    if redis.call("get", KEYS[1]) == ARGV[1] then
        return redis.call("del", KEYS[1])
    else
        return 0
    end
`)

func (l *DistributedLock) Release(ctx context.Context) error {
    return releaseLockScript.Run(ctx, l.rdb, []string{l.key}, l.token).Err()
}
```

## 7. Pub/Sub

```go
// Publisher
err := rdb.Publish(ctx, "orders:events", payload).Err()

// Subscriber
sub := rdb.Subscribe(ctx, "orders:events", "users:events")
defer sub.Close()

ch := sub.Channel()
for msg := range ch {
    fmt.Println(msg.Channel, msg.Payload)
    // check ctx.Done() if you need to stop
}
```

## 8. Redis Streams

```go
// Produce to stream
id, err := rdb.XAdd(ctx, &redis.XAddArgs{
    Stream: "orders",
    MaxLen: 10000,        // cap stream length
    Approx: true,         // approximate trimming (faster)
    Values: map[string]any{
        "order_id":   orderID,
        "event_type": "order.created",
        "payload":    string(jsonPayload),
    },
}).Result()

// Consumer group
rdb.XGroupCreateMkStream(ctx, "orders", "order-service", "$")

// Read
msgs, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
    Group:    "order-service",
    Consumer: "instance-1",
    Streams:  []string{"orders", ">"},  // ">" = new messages
    Count:    10,
    Block:    2 * time.Second,
}).Result()

// Acknowledge
rdb.XAck(ctx, "orders", "order-service", msg.ID)
```

## Redis Checklist
- [ ] Always set TTL on cache entries — never store without expiry
- [ ] `redis.Nil` checked explicitly for "key not found" (not treated as error)
- [ ] Distributed locks released with Lua script (atomic check+delete)
- [ ] Pipeline used for batch reads/writes (reduce round trips)
- [ ] WATCH+MULTI/EXEC with retry for concurrent counter updates
- [ ] Connection pool sized: 10 connections per service instance (tune per load)
- [ ] `Ping` on startup to verify connectivity before serving traffic
- [ ] Streams used for durable pub/sub (vs Pub/Sub which loses messages on disconnect)
