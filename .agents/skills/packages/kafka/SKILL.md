---
name: pkg-kafka
description: >
  Exhaustive reference for github.com/segmentio/kafka-go: producer setup, consumer groups,
  message serialization, offset management, error handling, schema registry integration,
  exactly-once semantics, dead letter queues, and testing patterns. Cross-references:
  event-driven/SKILL.md, packages/otel/SKILL.md.
---

# Package: segmentio/kafka-go — Complete Reference

## Import
```go
import "github.com/segmentio/kafka-go"
```

## 1. Producer (Writer)

```go
func NewKafkaWriter(brokers []string, topic string) *kafka.Writer {
    return &kafka.Writer{
        Addr:                   kafka.TCP(brokers...),
        Topic:                  topic,
        Balancer:               &kafka.Hash{},        // key-based partitioning
        MaxAttempts:            3,
        WriteBackoffMin:        100 * time.Millisecond,
        WriteBackoffMax:        1 * time.Second,
        BatchSize:              100,                   // batch up to 100 messages
        BatchTimeout:           10 * time.Millisecond, // flush every 10ms
        RequiredAcks:           kafka.RequireAll,      // all ISR must ack (durability)
        Async:                  false,                 // sync write — caller knows of errors
        Compression:            kafka.Snappy,          // compress batches
        AllowAutoTopicCreation: false,                 // topics must exist — explicit management
        Logger:      kafka.LoggerFunc(func(msg string, a ...any) { slog.Debug(fmt.Sprintf(msg, a...)) }),
        ErrorLogger: kafka.LoggerFunc(func(msg string, a ...any) { slog.Error(fmt.Sprintf(msg, a...)) }),
    }
}

// Publish with key (key determines partition — use entity ID for ordering guarantee)
func (p *KafkaProducer) Publish(ctx context.Context, event DomainEvent) error {
    payload, err := json.Marshal(event)
    if err != nil { return fmt.Errorf("Publish.marshal(%s): %w", event.EventName(), err) }

    envelope := CloudEvent{
        ID:      uuid.New().String(),
        Type:    event.EventName(),
        Source:  "/order-service",
        Subject: event.AggregateID(),
        Time:    event.OccurredAt(),
        Data:    payload,
    }
    envelopeBytes, _ := json.Marshal(envelope)

    return p.writer.WriteMessages(ctx, kafka.Message{
        Key:   []byte(event.AggregateID()), // ensures ordering per entity
        Value: envelopeBytes,
        Headers: []kafka.Header{
            {Key: "content-type", Value: []byte("application/json")},
            {Key: "event-type",   Value: []byte(event.EventName())},
        },
    })
}

// Batch publish
func (p *KafkaProducer) PublishBatch(ctx context.Context, events []DomainEvent) error {
    msgs := make([]kafka.Message, len(events))
    for i, evt := range events {
        payload, err := json.Marshal(evt)
        if err != nil { return fmt.Errorf("PublishBatch[%d].marshal: %w", i, err) }
        msgs[i] = kafka.Message{Key: []byte(evt.AggregateID()), Value: payload}
    }
    return p.writer.WriteMessages(ctx, msgs...)
}
```

## 2. Consumer Group

```go
func NewKafkaReader(brokers []string, topic, groupID string) *kafka.Reader {
    return kafka.NewReader(kafka.ReaderConfig{
        Brokers:        brokers,
        Topic:          topic,
        GroupID:        groupID,  // consumer group — enables auto partition assignment
        MinBytes:       1,        // fetch immediately if any data available
        MaxBytes:       10 << 20, // 10 MB max fetch size
        MaxWait:        250 * time.Millisecond,
        CommitInterval: time.Second, // auto-commit offsets every 1s (at-least-once)
        // For exactly-once: use CommitInterval=0 and commit manually
        StartOffset:    kafka.LastOffset, // start from latest (use FirstOffset for replay)
        RetentionTime:  7 * 24 * time.Hour,
        Logger:      kafka.LoggerFunc(func(msg string, a ...any) { slog.Debug(fmt.Sprintf(msg, a...)) }),
        ErrorLogger: kafka.LoggerFunc(func(msg string, a ...any) { slog.Error(fmt.Sprintf(msg, a...)) }),
    })
}

// Consumer loop — idempotent handler + DLQ on max retries
type KafkaConsumer struct {
    reader   *kafka.Reader
    handler  MessageHandler
    dlqWriter *kafka.Writer
    maxRetry int
    logger   *slog.Logger
}

func (c *KafkaConsumer) Run(ctx context.Context) error {
    for {
        msg, err := c.reader.FetchMessage(ctx)
        if err != nil {
            if ctx.Err() != nil { return nil }  // context cancelled — shutdown
            return fmt.Errorf("KafkaConsumer.Fetch: %w", err)
        }

        if err := c.processWithRetry(ctx, msg); err != nil {
            c.logger.ErrorContext(ctx, "message failed — sending to DLQ",
                slog.String("topic", msg.Topic),
                slog.Int("partition", msg.Partition),
                slog.Int64("offset", msg.Offset),
                slog.Any("err", err),
            )
            if dlqErr := c.sendToDLQ(ctx, msg, err); dlqErr != nil {
                c.logger.ErrorContext(ctx, "DLQ write failed", slog.Any("err", dlqErr))
            }
        }

        // Commit offset AFTER processing (at-least-once delivery)
        if err := c.reader.CommitMessages(ctx, msg); err != nil {
            return fmt.Errorf("KafkaConsumer.Commit: %w", err)
        }
    }
}

func (c *KafkaConsumer) processWithRetry(ctx context.Context, msg kafka.Message) error {
    for attempt := 1; attempt <= c.maxRetry; attempt++ {
        err := c.handler.Handle(ctx, msg)
        if err == nil { return nil }
        if !isRetryable(err) { return err }  // non-retryable: send straight to DLQ

        backoff := time.Duration(attempt) * 100 * time.Millisecond
        c.logger.WarnContext(ctx, "message processing failed, retrying",
            slog.Int("attempt", attempt), slog.Duration("backoff", backoff), slog.Any("err", err))
        select {
        case <-time.After(backoff):
        case <-ctx.Done(): return ctx.Err()
        }
    }
    return fmt.Errorf("max retries (%d) exceeded", c.maxRetry)
}

func (c *KafkaConsumer) sendToDLQ(ctx context.Context, original kafka.Message, cause error) error {
    return c.dlqWriter.WriteMessages(ctx, kafka.Message{
        Key:   original.Key,
        Value: original.Value,
        Headers: append(original.Headers,
            kafka.Header{Key: "dlq-error",       Value: []byte(cause.Error())},
            kafka.Header{Key: "dlq-source-topic", Value: []byte(original.Topic)},
            kafka.Header{Key: "dlq-failed-at",    Value: []byte(time.Now().UTC().Format(time.RFC3339))},
        ),
    })
}

func isRetryable(err error) bool {
    return !errors.Is(err, domain.ErrValidation) && !errors.Is(err, ErrMalformedMessage)
}
```

## 3. Manual Offset Commit (Exactly-Once with DB)

```go
// For exactly-once: commit offset only after DB transaction succeeds
// reader must have CommitInterval: 0 (disable auto-commit)
func (c *ExactlyOnceConsumer) Run(ctx context.Context) error {
    for {
        msg, err := c.reader.FetchMessage(ctx)
        if err != nil { if ctx.Err() != nil { return nil }; return err }

        // Process + persist in same DB transaction
        err = c.db.WithTx(ctx, func(ctx context.Context) error {
            return c.handler.HandleWithTx(ctx, msg)
        })
        if err != nil { /* handle */ continue }

        // Commit offset only after successful DB commit
        if err := c.reader.CommitMessages(ctx, msg); err != nil {
            return fmt.Errorf("commit offset: %w", err)
        }
    }
}
```

## 4. Admin Operations

```go
// Create topic programmatically
conn, _ := kafka.Dial("tcp", brokers[0])
defer conn.Close()

controller, _ := conn.Controller()
controlConn, _ := kafka.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
defer controlConn.Close()

err := controlConn.CreateTopics(kafka.TopicConfig{
    Topic:             "orders",
    NumPartitions:     12,
    ReplicationFactor: 3,
})
```

## Kafka Checklist
- [ ] `RequiredAcks: kafka.RequireAll` — all ISR ack for durability
- [ ] `AllowAutoTopicCreation: false` — topics managed explicitly
- [ ] Messages keyed by entity ID — ensures ordering per entity
- [ ] Consumer handles at-least-once delivery — handler is idempotent
- [ ] DLQ configured for messages failing after maxRetry
- [ ] Offset committed AFTER successful processing (not before)
- [ ] `reader.Close()` on shutdown — releases partition assignments
- [ ] `writer.Close()` on shutdown — flushes pending messages
- [ ] CloudEvents envelope used for portable event schema
- [ ] Consumer lag monitored as key SLI (see observability/SKILL.md)
