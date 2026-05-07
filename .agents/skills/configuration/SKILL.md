---
name: golang-configuration
description: >
  Go configuration management: 12-factor config, environment-based config, strongly-typed
  config structs, validation on startup, hot reload, secret injection, config layering
  (defaults → file → env → flags), feature flags, and config-as-code patterns.
  Always combine with packages/cobra-viper/SKILL.md for CLI+config integration.
---

# Go Configuration Management

## 1. Strongly-Typed Config Struct

```go
// config/config.go — single source of truth for all configuration
package config

import (
    "time"
    "github.com/go-playground/validator/v10"
)

// Config is the root configuration struct.
// All values have sensible defaults. Required values are tagged validate:"required".
type Config struct {
    Server   ServerConfig   `mapstructure:"server"`
    Database DatabaseConfig `mapstructure:"database"`
    Redis    RedisConfig    `mapstructure:"redis"`
    Auth     AuthConfig     `mapstructure:"auth"`
    Log      LogConfig      `mapstructure:"log"`
    Feature  FeatureFlags   `mapstructure:"feature"`
}

type ServerConfig struct {
    Addr            string        `mapstructure:"addr"              default:":8080"`
    ReadTimeout     time.Duration `mapstructure:"read_timeout"      default:"15s"`
    WriteTimeout    time.Duration `mapstructure:"write_timeout"     default:"15s"`
    IdleTimeout     time.Duration `mapstructure:"idle_timeout"      default:"120s"`
    ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"  default:"30s"`
    TLSCertFile     string        `mapstructure:"tls_cert_file"`
    TLSKeyFile      string        `mapstructure:"tls_key_file"`
}

type DatabaseConfig struct {
    DSN             string        `mapstructure:"dsn"               validate:"required"`
    MaxConns        int32         `mapstructure:"max_conns"         default:"25"`
    MinConns        int32         `mapstructure:"min_conns"         default:"5"`
    MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime" default:"1h"`
    MaxConnIdle     time.Duration `mapstructure:"max_conn_idle"     default:"30m"`
}

type AuthConfig struct {
    JWTPublicKeyPath  string        `mapstructure:"jwt_public_key_path"  validate:"required"`
    JWTPrivateKeyPath string        `mapstructure:"jwt_private_key_path"` // only needed by auth service
    TokenTTL          time.Duration `mapstructure:"token_ttl"            default:"24h"`
    RefreshTTL        time.Duration `mapstructure:"refresh_ttl"          default:"720h"`
}

type LogConfig struct {
    Level  string `mapstructure:"level"  default:"info"`  // debug|info|warn|error
    Format string `mapstructure:"format" default:"json"`  // json|text
}

type FeatureFlags struct {
    EnableNewCheckout bool `mapstructure:"enable_new_checkout" default:"false"`
    EnableBetaSearch  bool `mapstructure:"enable_beta_search"  default:"false"`
}
```

---

## 2. Load with Viper (12-Factor Layering)

```go
// config/loader.go
package config

import (
    "fmt"
    "strings"
    "github.com/spf13/viper"
    "github.com/go-playground/validator/v10"
)

// Load applies config in priority order: defaults < file < env < flags
// Env vars: APP_SERVER_ADDR, APP_DATABASE_DSN, etc. (prefix=APP, separator=_)
func Load(cfgFile string) (*Config, error) {
    v := viper.New()

    // 1. Defaults
    setDefaults(v)

    // 2. Config file (optional)
    if cfgFile != "" {
        v.SetConfigFile(cfgFile)
    } else {
        v.AddConfigPath(".")
        v.AddConfigPath("/etc/myapp")
        v.SetConfigName("config")
        v.SetConfigType("yaml")
    }
    if err := v.ReadInConfig(); err != nil {
        if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
            return nil, fmt.Errorf("config.Load: read file: %w", err)
        }
        // Config file not found — OK, use env/defaults
    }

    // 3. Environment variables (highest priority, except explicit flags)
    v.SetEnvPrefix("APP")
    v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
    v.AutomaticEnv()

    // 4. Unmarshal into struct
    var cfg Config
    if err := v.Unmarshal(&cfg); err != nil {
        return nil, fmt.Errorf("config.Load: unmarshal: %w", err)
    }

    // 5. Validate — fail fast on startup if config is invalid
    validate := validator.New()
    if err := validate.Struct(&cfg); err != nil {
        return nil, fmt.Errorf("config.Load: validation: %w", err)
    }

    return &cfg, nil
}

func setDefaults(v *viper.Viper) {
    v.SetDefault("server.addr", ":8080")
    v.SetDefault("server.read_timeout", "15s")
    v.SetDefault("server.write_timeout", "15s")
    v.SetDefault("server.idle_timeout", "120s")
    v.SetDefault("server.shutdown_timeout", "30s")
    v.SetDefault("database.max_conns", 25)
    v.SetDefault("database.min_conns", 5)
    v.SetDefault("database.max_conn_lifetime", "1h")
    v.SetDefault("log.level", "info")
    v.SetDefault("log.format", "json")
}
```

---

## 3. Config File Template

```yaml
# config/config.yaml — committed to repo (NO secrets — those come from env/vault)
server:
  addr: ":8080"
  read_timeout: "15s"
  write_timeout: "15s"
  shutdown_timeout: "30s"

database:
  max_conns: 25
  min_conns: 5
  max_conn_lifetime: "1h"
  # DSN comes from APP_DATABASE_DSN env var — never in config file

auth:
  token_ttl: "24h"
  refresh_ttl: "720h"
  # jwt_public_key_path from APP_AUTH_JWT_PUBLIC_KEY_PATH

log:
  level: "info"    # override to "debug" in dev
  format: "json"   # override to "text" in dev

feature:
  enable_new_checkout: false
  enable_beta_search: false
```

---

## 4. Hot Reload

```go
// Watch for config changes and reload (e.g., feature flags)
type ConfigWatcher struct {
    mu      sync.RWMutex
    cfg     *Config
    viper   *viper.Viper
    onChange func(old, new *Config)
}

func (w *ConfigWatcher) Watch() {
    w.viper.OnConfigChange(func(e fsnotify.Event) {
        newCfg := &Config{}
        if err := w.viper.Unmarshal(newCfg); err != nil {
            slog.Error("config reload failed", "err", err)
            return
        }
        validate := validator.New()
        if err := validate.Struct(newCfg); err != nil {
            slog.Error("config reload validation failed", "err", err)
            return
        }
        w.mu.Lock()
        old := w.cfg
        w.cfg = newCfg
        w.mu.Unlock()

        slog.Info("config reloaded")
        if w.onChange != nil { w.onChange(old, newCfg) }
    })
    w.viper.WatchConfig()
}

func (w *ConfigWatcher) Get() *Config {
    w.mu.RLock(); defer w.mu.RUnlock()
    return w.cfg
}

// Feature flags accessed through watcher — always current
func (w *ConfigWatcher) FeatureEnabled(flag string) bool {
    cfg := w.Get()
    switch flag {
    case "new_checkout": return cfg.Feature.EnableNewCheckout
    default:             return false
    }
}
```

---

## 5. Secret Injection Pattern

```go
// Secrets NEVER in config files or committed code.
// Sources: env vars (simple), HashiCorp Vault (production), AWS SSM/Secrets Manager

type SecretProvider interface {
    GetSecret(ctx context.Context, name string) (string, error)
}

// Compose: load non-secret config from viper, then inject secrets
func LoadWithSecrets(cfgFile string, secrets SecretProvider) (*Config, error) {
    cfg, err := Load(cfgFile)
    if err != nil { return nil, err }

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    // Inject DSN (never in config file)
    dsn, err := secrets.GetSecret(ctx, "database/dsn")
    if err != nil { return nil, fmt.Errorf("LoadWithSecrets: get DSN: %w", err) }
    cfg.Database.DSN = dsn

    return cfg, nil
}
```

---

## Config Checklist

- [ ] Zero secrets in config files or environment variable documentation examples
- [ ] All config in one typed struct — no scattered `os.Getenv` calls
- [ ] `validate:"required"` on all required fields; validated at startup
- [ ] Config fails fast: process exits if invalid config, with clear error message
- [ ] Default values defined for all optional fields
- [ ] Env vars prefixed (e.g., `APP_`) to avoid collision
- [ ] Feature flags in config — hot-reloadable without restart
- [ ] Secret injection separate from non-secret config loading
- [ ] Config struct documented with unit for each duration/size field
