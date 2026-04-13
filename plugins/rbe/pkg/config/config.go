// Package config provides centralised configuration loading via Viper.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root configuration structure.
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Metadata MetadataConfig `mapstructure:"metadata"`
	Auth     AuthConfig     `mapstructure:"auth"`
	Registry RegistryConfig `mapstructure:"registry"`
	Cache    CacheConfig    `mapstructure:"cache"`
	Logs     LogsConfig     `mapstructure:"logs"`
	Observability ObsConfig `mapstructure:"observability"`
}

type ServerConfig struct {
	HTTPAddr        string        `mapstructure:"http_addr"`
	GRPCAddr        string        `mapstructure:"grpc_addr"`
	TLSCertFile     string        `mapstructure:"tls_cert_file"`
	TLSKeyFile      string        `mapstructure:"tls_key_file"`
	ClientCACert    string        `mapstructure:"client_ca_cert"`
	RequireMTLS     bool          `mapstructure:"require_mtls"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	MaxRequestSize  int64         `mapstructure:"max_request_size"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
}

type StorageConfig struct {
	Backend  string      `mapstructure:"backend"` // "s3" or "local"
	S3       S3Config    `mapstructure:"s3"`
	Local    LocalConfig `mapstructure:"local"`
}

type S3Config struct {
	Endpoint        string `mapstructure:"endpoint"`
	Region          string `mapstructure:"region"`
	Bucket          string `mapstructure:"bucket"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
	ForcePathStyle  bool   `mapstructure:"force_path_style"`
	KeyPrefix       string `mapstructure:"key_prefix"`
	UploadConcurrency int  `mapstructure:"upload_concurrency"`
}

type LocalConfig struct {
	Root string `mapstructure:"root"`
}

type MetadataConfig struct {
	Backend  string      `mapstructure:"backend"` // "etcd", "tikv", "fdb"
	Etcd     EtcdConfig  `mapstructure:"etcd"`
	TiKV     TiKVConfig  `mapstructure:"tikv"`
}

type EtcdConfig struct {
	Endpoints   []string      `mapstructure:"endpoints"`
	DialTimeout time.Duration `mapstructure:"dial_timeout"`
	Username    string        `mapstructure:"username"`
	Password    string        `mapstructure:"password"`
	CertFile    string        `mapstructure:"cert_file"`
	KeyFile     string        `mapstructure:"key_file"`
	CAFile      string        `mapstructure:"ca_file"`
}

type TiKVConfig struct {
	PDAddresses []string `mapstructure:"pd_addresses"`
}

type AuthConfig struct {
	JWTSigningKey  string        `mapstructure:"jwt_signing_key"`
	JWTPublicKeys  []string      `mapstructure:"jwt_public_keys"`
	OIDCIssuer     string        `mapstructure:"oidc_issuer"`
	OIDCAudience   string        `mapstructure:"oidc_audience"`
	JWKSEndpoint   string        `mapstructure:"jwks_endpoint"`
	ClientCACert   string        `mapstructure:"client_ca_cert"`
	RequireMTLS    bool          `mapstructure:"require_mtls"`
	APIKeyHeader   string        `mapstructure:"api_key_header"`
	TokenTTL       time.Duration `mapstructure:"token_ttl"`
}

type RegistryConfig struct {
	DefaultNamespace string `mapstructure:"default_namespace"`
	MaxBlobSize      int64  `mapstructure:"max_blob_size"`
	UploadExpiry     time.Duration `mapstructure:"upload_expiry"`
	EnableReferrers  bool   `mapstructure:"enable_referrers"`
}

type CacheConfig struct {
	DefaultTTL time.Duration `mapstructure:"default_ttl"`
	MaxEntries int64         `mapstructure:"max_entries"`
}

type LogsConfig struct {
	RetentionDays int   `mapstructure:"retention_days"`
	MaxChunkBytes int64 `mapstructure:"max_chunk_bytes"`
}

type ObsConfig struct {
	OTLPEndpoint    string `mapstructure:"otlp_endpoint"`
	PrometheusAddr  string `mapstructure:"prometheus_addr"`
	LogLevel        string `mapstructure:"log_level"`
	ServiceName     string `mapstructure:"service_name"`
	ServiceVersion  string `mapstructure:"service_version"`
}

// Load reads configuration from the given file path and environment variables.
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("server.http_addr", ":8080")
	v.SetDefault("server.grpc_addr", ":9090")
	v.SetDefault("server.shutdown_timeout", "30s")
	v.SetDefault("server.max_request_size", 32*1024*1024*1024) // 32 GiB
	v.SetDefault("server.read_timeout", "60s")
	v.SetDefault("server.write_timeout", "3600s") // large uploads
	v.SetDefault("storage.backend", "local")
	v.SetDefault("storage.local.root", "/var/lib/rbed/blobs")
	v.SetDefault("metadata.backend", "etcd")
	v.SetDefault("metadata.etcd.endpoints", []string{"localhost:2379"})
	v.SetDefault("metadata.etcd.dial_timeout", "5s")
	v.SetDefault("auth.api_key_header", "X-RBE-API-Key")
	v.SetDefault("auth.token_ttl", "24h")
	v.SetDefault("registry.upload_expiry", "1h")
	v.SetDefault("registry.enable_referrers", true)
	v.SetDefault("cache.default_ttl", "720h") // 30 days
	v.SetDefault("logs.retention_days", 90)
	v.SetDefault("logs.max_chunk_bytes", 1*1024*1024) // 1 MiB
	v.SetDefault("observability.log_level", "info")
	v.SetDefault("observability.service_name", "rbed")
	v.SetDefault("observability.service_version", "0.1.0")

	// Environment overrides (RBE_ prefix)
	v.SetEnvPrefix("RBE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Config file
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		v.SetConfigName("rbed")
		v.SetConfigType("yaml")
		v.AddConfigPath("/etc/rbed")
		v.AddConfigPath("$HOME/.rbed")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("config: read %s: %w", cfgFile, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}
	return &cfg, nil
}
