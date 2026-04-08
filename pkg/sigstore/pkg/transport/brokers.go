// Package transport provides extension hooks for replacing the in-memory
// MemoryBus with external message brokers.
//
// EXTENSION GUIDE — Swapping the EventBus backend:
//
//  1. Implement the eventbus.EventBus interface (Publish + Subscribe + Close).
//  2. Register the new type in bootstrap/wire.go:
//     if cfg.EventBus.Backend == "kafka" {
//     bus = transport.NewKafkaBus(...)
//     }
//  3. Zero changes to signing_service.go, resilience, or any domain code.
//
// This file contains buildable skeletons — replace TODOs with the actual
// client SDK calls from the chosen broker.
package transport

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
	"github.com/bons/bons-ci/pkg/sigstore/internal/eventbus"
)

// ══════════════════════════════════════════════════════════════════════════════
// Kafka Bus Skeleton
// Dependencies: github.com/segmentio/kafka-go or confluent-kafka-go
// ══════════════════════════════════════════════════════════════════════════════

// KafkaBusConfig holds Kafka connection parameters.
// Credentials must come from environment variables or a secret manager.
type KafkaBusConfig struct {
	// Brokers is the list of bootstrap broker addresses.
	// NEVER hardcode; inject from environment/config.
	Brokers []string

	// GroupID is the consumer group name for distributed consumption.
	GroupID string

	// TopicPrefix allows multi-tenant deployments on shared clusters.
	// e.g. "prod.signing." → topic "prod.signing.signing.requested"
	TopicPrefix string

	Logger *slog.Logger
}

// KafkaBus implements eventbus.EventBus over Apache Kafka.
// Uncommenting and completing the TODO sections yields a production-ready
// Kafka bus without changing any other package.
//
// PRODUCTION TODO:
//  1. Import kafka client: "github.com/segmentio/kafka-go"
//  2. Replace stub reader/writer with kafka.NewReader / kafka.NewWriter
//  3. Serialise domain.Envelope to JSON in Publish; deserialise in subscriber goroutine
//  4. Use Kafka consumer group offset management for at-least-once delivery
type KafkaBus struct {
	cfg    KafkaBusConfig
	closed chan struct{}
	// TODO: writer *kafka.Writer
	// TODO: readers map[domain.EventType]*kafka.Reader
}

// NewKafkaBus constructs a KafkaBus (STUB — not functional until TODO completed).
func NewKafkaBus(cfg KafkaBusConfig) (*KafkaBus, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka bus: at least one broker required")
	}
	return &KafkaBus{cfg: cfg, closed: make(chan struct{})}, nil
}

func (b *KafkaBus) Publish(_ context.Context, env domain.Envelope) error {
	select {
	case <-b.closed:
		return eventbus.ErrBusClosed{}
	default:
	}
	topic := b.cfg.TopicPrefix + string(env.Topic)
	b.cfg.Logger.Debug("kafka publish (stub)", "topic", topic)
	// TODO: marshal env to JSON and write to Kafka topic
	return fmt.Errorf("kafka bus: STUB — implement kafka writer for topic %q", topic)
}

func (b *KafkaBus) Subscribe(topic domain.EventType, _ eventbus.Handler) (eventbus.Subscription, error) {
	select {
	case <-b.closed:
		return nil, eventbus.ErrBusClosed{}
	default:
	}
	kafkaTopic := b.cfg.TopicPrefix + string(topic)
	b.cfg.Logger.Debug("kafka subscribe (stub)", "topic", kafkaTopic)
	// TODO: create kafka.Reader for kafkaTopic, start consumer goroutine
	return nil, fmt.Errorf("kafka bus: STUB — implement kafka reader for topic %q", kafkaTopic)
}

func (b *KafkaBus) Close() error {
	close(b.closed)
	// TODO: close writer and all readers
	return nil
}

// ══════════════════════════════════════════════════════════════════════════════
// NATS / JetStream Bus Skeleton
// Dependencies: github.com/nats-io/nats.go
// ══════════════════════════════════════════════════════════════════════════════

// NATSBusConfig holds NATS connection parameters.
type NATSBusConfig struct {
	// URL is the NATS server URL.
	// Example: "nats://nats:4222"
	URL string

	// StreamName is the JetStream stream for durable message persistence.
	StreamName string

	// ConsumerDurable is the durable consumer name for exactly-once delivery.
	ConsumerDurable string

	Logger *slog.Logger
}

// NATSBus implements eventbus.EventBus over NATS JetStream.
//
// PRODUCTION TODO:
//  1. Import: "github.com/nats-io/nats.go"
//  2. Connect: nats.Connect(cfg.URL, nats.Name("signing-service"))
//  3. Create JetStream context: nc.JetStream()
//  4. Publish: js.Publish(subject, data)
//  5. Subscribe: js.Subscribe(subject, handler, nats.Durable(cfg.ConsumerDurable))
type NATSBus struct {
	cfg    NATSBusConfig
	closed chan struct{}
	// TODO: nc *nats.Conn
	// TODO: js nats.JetStreamContext
}

// NewNATSBus constructs a NATSBus (STUB — not functional until TODO completed).
func NewNATSBus(cfg NATSBusConfig) (*NATSBus, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("nats bus: URL is required")
	}
	return &NATSBus{cfg: cfg, closed: make(chan struct{})}, nil
}

func (b *NATSBus) Publish(_ context.Context, env domain.Envelope) error {
	b.cfg.Logger.Debug("nats publish (stub)", "topic", env.Topic)
	return fmt.Errorf("nats bus: STUB — implement NATS publish for topic %q", env.Topic)
}

func (b *NATSBus) Subscribe(topic domain.EventType, _ eventbus.Handler) (eventbus.Subscription, error) {
	b.cfg.Logger.Debug("nats subscribe (stub)", "topic", topic)
	return nil, fmt.Errorf("nats bus: STUB — implement NATS subscribe for topic %q", topic)
}

func (b *NATSBus) Close() error {
	close(b.closed)
	// TODO: b.nc.Drain()
	return nil
}

// ══════════════════════════════════════════════════════════════════════════════
// Google Cloud Pub/Sub Bus Skeleton
// Dependencies: cloud.google.com/go/pubsub
// ══════════════════════════════════════════════════════════════════════════════

// PubSubBusConfig holds Google Cloud Pub/Sub configuration.
type PubSubBusConfig struct {
	ProjectID string
	Logger    *slog.Logger
}

// PubSubBus implements eventbus.EventBus over Google Cloud Pub/Sub.
//
// PRODUCTION TODO:
//  1. Import: "cloud.google.com/go/pubsub"
//  2. Create client: pubsub.NewClient(ctx, cfg.ProjectID)
//  3. Publish: topic.Publish(ctx, &pubsub.Message{Data: data})
//  4. Subscribe: sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {...})
type PubSubBus struct {
	cfg    PubSubBusConfig
	closed chan struct{}
}

func NewPubSubBus(cfg PubSubBusConfig) (*PubSubBus, error) {
	if cfg.ProjectID == "" {
		return nil, fmt.Errorf("pubsub bus: ProjectID is required")
	}
	return &PubSubBus{cfg: cfg, closed: make(chan struct{})}, nil
}

func (b *PubSubBus) Publish(_ context.Context, env domain.Envelope) error {
	return fmt.Errorf("pubsub bus: STUB — implement for topic %q", env.Topic)
}

func (b *PubSubBus) Subscribe(topic domain.EventType, _ eventbus.Handler) (eventbus.Subscription, error) {
	return nil, fmt.Errorf("pubsub bus: STUB — implement for topic %q", topic)
}

func (b *PubSubBus) Close() error {
	close(b.closed)
	return nil
}
