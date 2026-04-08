// Package config provides zero-implicit-default configuration loading.
// All values are explicit: missing required fields cause startup failure.
// No environment-specific logic is embedded; callers select config files
// via the CONFIG_PATH environment variable.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration struct. It mirrors the YAML schema 1:1.
// All durations are time.Duration for type safety; YAML values use Go syntax
// (e.g. "30s", "2m").
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Signing    SigningConfig    `yaml:"signing"`
	EventBus   EventBusConfig   `yaml:"event_bus"`
	Resilience ResilienceConfig `yaml:"resilience"`
	Observability ObservabilityConfig `yaml:"observability"`
}

type ServerConfig struct {
	// Addr is the listen address for the HTTP server.
	// Example: ":8080"
	Addr string `yaml:"addr"`

	// ShutdownTimeout is the graceful shutdown budget.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type SigningConfig struct {
	// Backend selects the signing implementation: "keyless" | "static_key" | "kms"
	Backend string `yaml:"backend"`

	// KeylessConfig is used when Backend == "keyless"
	Keyless KeylessConfig `yaml:"keyless"`

	// StaticKeyConfig is used when Backend == "static_key"
	StaticKey StaticKeyConfig `yaml:"static_key"`

	// KMSConfig is used when Backend == "kms"
	KMS KMSConfig `yaml:"kms"`
}

type KeylessConfig struct {
	FulcioURL    string `yaml:"fulcio_url"`
	RekorURL     string `yaml:"rekor_url"`
	OIDCIssuer   string `yaml:"oidc_issuer"`
	OIDCClientID string `yaml:"oidc_client_id"`
}

type StaticKeyConfig struct {
	// KeyPath is the filesystem path to the PEM-encoded ECDSA private key.
	// In production, mount this from a Kubernetes secret or KMS.
	KeyPath  string `yaml:"key_path"`
	KeyName  string `yaml:"key_name"`
	RekorURL string `yaml:"rekor_url"`
}

type KMSConfig struct {
	// KMSPath follows the Cosign convention:
	// gcpkms://projects/P/locations/L/keyRings/R/cryptoKeys/K/cryptoKeyVersions/V
	KMSPath  string `yaml:"kms_path"`
	RekorURL string `yaml:"rekor_url"`
}

type EventBusConfig struct {
	// BufferSize is the per-topic channel buffer capacity.
	BufferSize int `yaml:"buffer_size"`

	// WorkersPerTopic is the worker pool size per event topic.
	WorkersPerTopic int `yaml:"workers_per_topic"`

	// HandlerTimeout caps a single handler invocation.
	HandlerTimeout time.Duration `yaml:"handler_timeout"`

	// MaxHandlerRetries before dead-letter routing.
	MaxHandlerRetries int `yaml:"max_handler_retries"`
}

type ResilienceConfig struct {
	Retry          RetryConfig          `yaml:"retry"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

type RetryConfig struct {
	MaxAttempts int           `yaml:"max_attempts"`
	InitialWait time.Duration `yaml:"initial_wait"`
	MaxWait     time.Duration `yaml:"max_wait"`
}

type CircuitBreakerConfig struct {
	FailureThreshold int           `yaml:"failure_threshold"`
	SuccessThreshold int           `yaml:"success_threshold"`
	OpenDuration     time.Duration `yaml:"open_duration"`
}

type ObservabilityConfig struct {
	// LogLevel: "debug" | "info" | "warn" | "error"
	LogLevel string `yaml:"log_level"`
	// LogFormat: "json" | "text"
	LogFormat string `yaml:"log_format"`
	// MetricsAddr is the listen address for Prometheus /metrics.
	MetricsAddr string `yaml:"metrics_addr"`
	// OTLPEndpoint is the gRPC OTLP exporter endpoint. Empty disables tracing.
	OTLPEndpoint   string `yaml:"otlp_endpoint"`
	ServiceName    string `yaml:"service_name"`
	ServiceVersion string `yaml:"service_version"`
}

// Load reads and validates configuration from the path pointed to by
// CONFIG_PATH, falling back to "config.yaml". Environment variables override
// YAML values using the pattern described in ApplyEnvOverrides.
func Load() (*Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "config.yaml"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var cfg Config
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}

	ApplyEnvOverrides(&cfg)

	if err = Validate(&cfg); err != nil {
		return nil, fmt.Errorf("config: validation: %w", err)
	}

	return &cfg, nil
}

// ApplyEnvOverrides maps well-known environment variables onto config fields.
// This keeps 12-factor compatibility without a complex viper dependency.
// All secrets come from environment variables, never from config files.
//
// Supported overrides:
//
//	SERVER_ADDR            → cfg.Server.Addr
//	SIGNING_BACKEND        → cfg.Signing.Backend
//	FULCIO_URL             → cfg.Signing.Keyless.FulcioURL
//	REKOR_URL              → cfg.Signing.Keyless.RekorURL / StaticKey.RekorURL
//	OIDC_ISSUER            → cfg.Signing.Keyless.OIDCIssuer
//	SIGNING_KEY_PATH       → cfg.Signing.StaticKey.KeyPath
//	KMS_PATH               → cfg.Signing.KMS.KMSPath
//	OTLP_ENDPOINT          → cfg.Observability.OTLPEndpoint
func ApplyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("SIGNING_BACKEND"); v != "" {
		cfg.Signing.Backend = v
	}
	if v := os.Getenv("FULCIO_URL"); v != "" {
		cfg.Signing.Keyless.FulcioURL = v
	}
	if v := os.Getenv("REKOR_URL"); v != "" {
		cfg.Signing.Keyless.RekorURL = v
		cfg.Signing.StaticKey.RekorURL = v
		cfg.Signing.KMS.RekorURL = v
	}
	if v := os.Getenv("OIDC_ISSUER"); v != "" {
		cfg.Signing.Keyless.OIDCIssuer = v
	}
	if v := os.Getenv("SIGNING_KEY_PATH"); v != "" {
		cfg.Signing.StaticKey.KeyPath = v
	}
	if v := os.Getenv("KMS_PATH"); v != "" {
		cfg.Signing.KMS.KMSPath = v
	}
	if v := os.Getenv("OTLP_ENDPOINT"); v != "" {
		cfg.Observability.OTLPEndpoint = v
	}
}

// Validate applies strict field-level validation. Returns all violations
// concatenated rather than stopping at the first failure, for better DX.
func Validate(cfg *Config) error {
	var errs []string
	check := func(cond bool, msg string) {
		if !cond {
			errs = append(errs, msg)
		}
	}

	check(cfg.Server.Addr != "", "server.addr is required")
	check(cfg.Signing.Backend != "", "signing.backend is required")

	switch cfg.Signing.Backend {
	case "keyless":
		check(cfg.Signing.Keyless.FulcioURL != "", "signing.keyless.fulcio_url is required")
		check(cfg.Signing.Keyless.RekorURL != "", "signing.keyless.rekor_url is required")
		check(cfg.Signing.Keyless.OIDCIssuer != "", "signing.keyless.oidc_issuer is required")
	case "static_key":
		check(cfg.Signing.StaticKey.KeyPath != "", "signing.static_key.key_path is required")
	case "kms":
		check(cfg.Signing.KMS.KMSPath != "", "signing.kms.kms_path is required")
	default:
		check(false, fmt.Sprintf("signing.backend %q is unknown (valid: keyless, static_key, kms)",
			cfg.Signing.Backend))
	}

	check(cfg.Observability.ServiceName != "", "observability.service_name is required")

	if len(errs) > 0 {
		return fmt.Errorf("config validation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// LogLevel converts the string level to a slog.Level.
func (c ObservabilityConfig) SlogLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
