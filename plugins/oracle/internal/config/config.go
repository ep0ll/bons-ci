// Package config loads and validates all subsystem configuration.
package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// Root is the top-level configuration.
type Root struct {
	OCI       OCIConfig       `mapstructure:"oci"`
	Migration MigrationConfig `mapstructure:"migration"`
	State     StateConfig     `mapstructure:"state"`
	CRIU      CRIUConfig      `mapstructure:"criu"`
	WarmPool  WarmPoolConfig  `mapstructure:"warm_pool"`
	Telemetry TelemetryConfig `mapstructure:"telemetry"`
}

type OCIConfig struct {
	ConfigFilePath     string            `mapstructure:"config_file_path"`
	Profile            string            `mapstructure:"profile"`
	TenancyOCID        string            `mapstructure:"tenancy_ocid"`
	CompartmentOCID    string            `mapstructure:"compartment_ocid"`
	InstanceOCID       string            `mapstructure:"instance_ocid"`
	Region             string            `mapstructure:"region"`
	AvailabilityDomain string            `mapstructure:"availability_domain"`
	Shape              string            `mapstructure:"shape"`
	ShapeConfig        ShapeConfig       `mapstructure:"shape_config"`
	ImageOCID          string            `mapstructure:"image_ocid"`
	SubnetOCID         string            `mapstructure:"subnet_ocid"`
	NsgOCIDs           []string          `mapstructure:"nsg_ocids"`
	FreeformTags       map[string]string `mapstructure:"freeform_tags"`
	MetadataEndpoint   string            `mapstructure:"metadata_endpoint"`
	SSHAuthorizedKeys  string            `mapstructure:"ssh_authorized_keys"`
}

type ShapeConfig struct {
	OCPUs       float32 `mapstructure:"ocpus"`
	MemoryInGBs float32 `mapstructure:"memory_in_gbs"`
}

type MigrationConfig struct {
	IsSuccessor            bool          `mapstructure:"is_successor"`
	MonitorInterval        time.Duration `mapstructure:"monitor_interval"`
	FastMonitorInterval    time.Duration `mapstructure:"fast_monitor_interval"`
	MigrationBudget        time.Duration `mapstructure:"migration_budget"`
	CheckpointDir          string        `mapstructure:"checkpoint_dir"`
	SharedVolumeDevice     string        `mapstructure:"shared_volume_device"`
	SharedVolumeMountPath  string        `mapstructure:"shared_volume_mount_path"`
	SharedVolumeOCID       string        `mapstructure:"shared_volume_ocid"`
	ProcessFilter          string        `mapstructure:"process_filter"`
	MigratedCgroup         string        `mapstructure:"migrated_cgroup"`
	SuccessorLaunchTimeout time.Duration `mapstructure:"successor_launch_timeout"`
	VolumeDetachTimeout    time.Duration `mapstructure:"volume_detach_timeout"`
	MaxRetries             int           `mapstructure:"max_retries"`
	// ControlPort is the port the migrator listens on for inter-instance commands.
	ControlPort int `mapstructure:"control_port"`
}

type CRIUConfig struct {
	BinaryPath          string `mapstructure:"binary_path"`
	LeaveRunning        bool   `mapstructure:"leave_running"`
	TCPEstablished      bool   `mapstructure:"tcp_established"`
	ExternalUnixSockets bool   `mapstructure:"external_unix_sockets"`
	ShellJob            bool   `mapstructure:"shell_job"`
	FileLocks           bool   `mapstructure:"file_locks"`
	PreDumpIterations   int    `mapstructure:"pre_dump_iterations"`
	// DirtyConvergenceThreshold: stop pre-dump when dirty page ratio < this.
	// 0.04 = 4% means < 4% of pages dirtied per interval.
	DirtyConvergenceThreshold float64 `mapstructure:"dirty_convergence_threshold"`
	// PreFreezeMs: brief cgroup freeze before final dump (milliseconds).
	// Drains the memory write pipeline, reducing final dirty set.
	PreFreezeMs int `mapstructure:"pre_freeze_ms"`
	// PageServerAddr: "host:port" of the CRIU page server on the successor.
	// When set, memory pages stream directly TCP instead of via shared volume.
	PageServerAddr string `mapstructure:"page_server_addr"`
	// PageServerPort is the port to use if page server address is auto-discovered.
	PageServerPort int `mapstructure:"page_server_port"`
	// CompressImages: compress .img files with zstd before writing to shared volume.
	CompressImages bool `mapstructure:"compress_images"`
	// AutoDedup: tell CRIU to de-duplicate pages against parent pre-dump images.
	AutoDedup bool `mapstructure:"auto_dedup"`
}

// WarmPoolConfig controls the warm instance pre-heating pool.
type WarmPoolConfig struct {
	// Enabled controls whether the warm pool feature is active.
	Enabled bool `mapstructure:"enabled"`
	// MinSize is the number of warm (STOPPED) instances to maintain.
	MinSize int `mapstructure:"min_size"`
	// MaxAge is how long a warm instance can live before being replaced.
	MaxAge time.Duration `mapstructure:"max_age"`
	// MaintenanceInterval is how often to run prune + ensure.
	MaintenanceInterval time.Duration `mapstructure:"maintenance_interval"`
	// StatePath is where the pool state is persisted on the shared volume.
	StatePath string `mapstructure:"state_path"`
}

type StateConfig struct {
	LedgerPath string `mapstructure:"ledger_path"`
	LockPath   string `mapstructure:"lock_path"`
}

type TelemetryConfig struct {
	PrometheusAddr string `mapstructure:"prometheus_addr"`
	OTELEndpoint   string `mapstructure:"otel_endpoint"`
	ServiceName    string `mapstructure:"service_name"`
}

// Load reads configuration with env override and sensible defaults.
func Load() (*Root, error) {
	v := viper.New()

	// ── Defaults ────────────────────────────────────────────────────────────
	v.SetDefault("oci.metadata_endpoint", "http://169.254.169.254/opc/v2")
	v.SetDefault("oci.profile", "DEFAULT")

	v.SetDefault("migration.monitor_interval", 5*time.Second)
	v.SetDefault("migration.fast_monitor_interval", 500*time.Millisecond)
	v.SetDefault("migration.migration_budget", 90*time.Second)
	v.SetDefault("migration.checkpoint_dir", "/mnt/migration/checkpoints")
	v.SetDefault("migration.shared_volume_mount_path", "/mnt/migration")
	v.SetDefault("migration.process_filter", "cgroup")
	v.SetDefault("migration.migrated_cgroup", "/cicd/build")
	v.SetDefault("migration.successor_launch_timeout", 90*time.Second)
	v.SetDefault("migration.volume_detach_timeout", 30*time.Second)
	v.SetDefault("migration.max_retries", 5)
	v.SetDefault("migration.control_port", 7077)

	v.SetDefault("criu.binary_path", "/usr/sbin/criu")
	v.SetDefault("criu.leave_running", true)
	v.SetDefault("criu.tcp_established", true)
	v.SetDefault("criu.external_unix_sockets", true)
	v.SetDefault("criu.shell_job", true)
	v.SetDefault("criu.file_locks", true)
	v.SetDefault("criu.pre_dump_iterations", 5)            // max rounds (adaptive stops earlier)
	v.SetDefault("criu.dirty_convergence_threshold", 0.04) // 4%
	v.SetDefault("criu.pre_freeze_ms", 50)                 // 50ms pre-freeze
	v.SetDefault("criu.page_server_port", 27182)
	v.SetDefault("criu.compress_images", true)
	v.SetDefault("criu.auto_dedup", true)

	v.SetDefault("warm_pool.enabled", true)
	v.SetDefault("warm_pool.min_size", 1)
	v.SetDefault("warm_pool.max_age", 4*time.Hour)
	v.SetDefault("warm_pool.maintenance_interval", 30*time.Minute)
	v.SetDefault("warm_pool.state_path", "/mnt/migration/warm-pool.json")

	v.SetDefault("state.ledger_path", "/mnt/migration/ledger.json")
	v.SetDefault("state.lock_path", "/mnt/migration/.migrator.lock")
	v.SetDefault("telemetry.prometheus_addr", ":9090")
	v.SetDefault("telemetry.service_name", "oci-live-migrator")

	// ── Config file ─────────────────────────────────────────────────────────
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("/etc/oci-migrator/")
	v.AddConfigPath("$HOME/.oci-migrator/")
	v.AddConfigPath(".")
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
	}

	// ── Environment overrides ────────────────────────────────────────────────
	v.SetEnvPrefix("OCI_MIGRATOR")
	v.AutomaticEnv()

	var cfg Root
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, validate(&cfg)
}

func validate(cfg *Root) error {
	if cfg.OCI.CompartmentOCID == "" {
		return fmt.Errorf("oci.compartment_ocid is required")
	}
	if cfg.OCI.SubnetOCID == "" {
		return fmt.Errorf("oci.subnet_ocid is required")
	}
	if cfg.OCI.Shape == "" {
		return fmt.Errorf("oci.shape is required")
	}
	if cfg.Migration.SharedVolumeOCID == "" {
		return fmt.Errorf("migration.shared_volume_ocid is required")
	}
	if cfg.Migration.MigrationBudget > 110*time.Second {
		return fmt.Errorf("migration.migration_budget must be ≤ 110s")
	}
	if cfg.CRIU.DirtyConvergenceThreshold < 0 || cfg.CRIU.DirtyConvergenceThreshold > 1 {
		return fmt.Errorf("criu.dirty_convergence_threshold must be 0.0–1.0")
	}
	return nil
}
