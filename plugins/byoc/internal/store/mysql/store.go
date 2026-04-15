// Package mysql provides the MySQL/GORM adapter for the store.Store interface.
package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/rs/zerolog"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// compile-time interface check
var _ store.Store = (*Store)(nil)

// Store is the MySQL implementation of store.Store.
type Store struct {
	db     *gorm.DB
	logger zerolog.Logger
}

// Config holds MySQL connection parameters.
type Config struct {
	DSN             string        // e.g. user:pass@tcp(host:3306)/dbname?parseTime=true
	MaxOpenConns    int           // default 25
	MaxIdleConns    int           // default 10
	ConnMaxLifetime time.Duration // default 5 min
}

// New opens a MySQL connection, runs auto-migrations, and returns a Store.
func New(cfg Config, logger zerolog.Logger) (*Store, error) {
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = 25
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = 10
	}
	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = 5 * time.Minute
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("mysql: open connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("mysql: get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	s := &Store{db: db, logger: logger}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("mysql: migrate: %w", err)
	}

	return s, nil
}

// --- GORM model definitions (flat structs for the DB layer) ---

type tenantModel struct {
	ID               string `gorm:"primaryKey;size:36"`
	Name             string `gorm:"size:128;not null"`
	GitHubAppID      int64  `gorm:"not null"`
	GitHubInstallID  int64  `gorm:"not null"`
	GitHubOrgName    string `gorm:"size:128;not null"`
	WebhookSecret    string `gorm:"size:256;not null"`
	OCICompartmentID string `gorm:"size:256;not null"`
	OCISubnetID      string `gorm:"size:256;not null"`
	MaxRunners       int    `gorm:"not null;default:20"`
	MinWarmPool      int    `gorm:"not null;default:0"`
	IdleTimeoutSec   int    `gorm:"not null;default:300"`
	RunnerLabelsJSON string `gorm:"type:text"` // JSON array
	RunnerShape      string `gorm:"size:64"`
	RunnerOCPUs      float32
	RunnerMemoryGB   float32
	ProvisionerType  string `gorm:"size:32;default:'compute'"`
	Status           string `gorm:"size:32;not null;default:'active'"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (tenantModel) TableName() string { return "tenants" }

type runnerModel struct {
	ID              string `gorm:"primaryKey;size:36"`
	TenantID        string `gorm:"size:36;not null;index"`
	JobID           int64  `gorm:"not null;default:0;index"`
	GitHubRunnerID  int64  `gorm:"not null;default:0"`
	OCIInstanceID   string `gorm:"size:256"`
	ProvisionerType string `gorm:"size:32"`
	Status          string `gorm:"size:32;not null;index"`
	LabelsJSON      string `gorm:"type:text"`
	ProvisionedAt   time.Time
	RegisteredAt    *time.Time
	IdleSince       *time.Time
	TerminatedAt    *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (runnerModel) TableName() string { return "runners" }

type idempotencyLock struct {
	LockKey   string    `gorm:"primaryKey;size:256"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}

func (idempotencyLock) TableName() string { return "idempotency_locks" }

func (s *Store) migrate() error {
	return s.db.AutoMigrate(&tenantModel{}, &runnerModel{}, &idempotencyLock{})
}

// --- Tenant operations ---

func (s *Store) CreateTenant(ctx context.Context, t *store.Tenant) error {
	m := tenantToModel(t)
	result := s.db.WithContext(ctx).Create(m)
	if result.Error != nil {
		if isDuplicateError(result.Error) {
			return fmt.Errorf("create tenant %s: %w", t.ID, store.ErrConflict)
		}
		return fmt.Errorf("create tenant: %w", result.Error)
	}
	return nil
}

func (s *Store) GetTenant(ctx context.Context, id string) (*store.Tenant, error) {
	var m tenantModel
	result := s.db.WithContext(ctx).First(&m, "id = ?", id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("tenant %s: %w", id, store.ErrNotFound)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("get tenant %s: %w", id, result.Error)
	}
	return modelToTenant(&m), nil
}

func (s *Store) UpdateTenant(ctx context.Context, t *store.Tenant) error {
	m := tenantToModel(t)
	result := s.db.WithContext(ctx).Save(m)
	if result.Error != nil {
		return fmt.Errorf("update tenant %s: %w", t.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("update tenant %s: %w", t.ID, store.ErrNotFound)
	}
	return nil
}

func (s *Store) ListTenants(ctx context.Context, f store.TenantFilter) ([]*store.Tenant, error) {
	q := s.db.WithContext(ctx).Model(&tenantModel{})
	if f.Status != nil {
		q = q.Where("status = ?", string(*f.Status))
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if f.Offset > 0 {
		q = q.Offset(f.Offset)
	}
	var models []tenantModel
	if err := q.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	out := make([]*store.Tenant, 0, len(models))
	for i := range models {
		out = append(out, modelToTenant(&models[i]))
	}
	return out, nil
}

// --- Runner operations ---

func (s *Store) CreateRunner(ctx context.Context, r *store.Runner) error {
	m := runnerToModel(r)
	result := s.db.WithContext(ctx).Create(m)
	if result.Error != nil {
		if isDuplicateError(result.Error) {
			return fmt.Errorf("create runner %s: %w", r.ID, store.ErrConflict)
		}
		return fmt.Errorf("create runner: %w", result.Error)
	}
	return nil
}

func (s *Store) GetRunner(ctx context.Context, id string) (*store.Runner, error) {
	var m runnerModel
	result := s.db.WithContext(ctx).First(&m, "id = ?", id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("runner %s: %w", id, store.ErrNotFound)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("get runner %s: %w", id, result.Error)
	}
	return modelToRunner(&m), nil
}

func (s *Store) UpdateRunnerStatus(ctx context.Context, id string, status store.RunnerStatus, opts store.RunnerUpdateOpts) error {
	updates := map[string]interface{}{"status": string(status)}
	if opts.GitHubRunnerID != nil {
		updates["github_runner_id"] = *opts.GitHubRunnerID
	}
	if opts.OCIInstanceID != nil {
		updates["oci_instance_id"] = *opts.OCIInstanceID
	}
	if opts.RegisteredAt != nil {
		updates["registered_at"] = opts.RegisteredAt
	}
	if opts.IdleSince != nil {
		updates["idle_since"] = opts.IdleSince
	}
	if opts.TerminatedAt != nil {
		updates["terminated_at"] = opts.TerminatedAt
	}

	result := s.db.WithContext(ctx).Model(&runnerModel{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update runner status %s: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("runner %s: %w", id, store.ErrNotFound)
	}
	return nil
}

func (s *Store) ListRunners(ctx context.Context, f store.RunnerFilter) ([]*store.Runner, error) {
	q := s.db.WithContext(ctx).Model(&runnerModel{})
	if f.TenantID != nil {
		q = q.Where("tenant_id = ?", *f.TenantID)
	}
	if f.Status != nil {
		q = q.Where("status = ?", string(*f.Status))
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if f.Offset > 0 {
		q = q.Offset(f.Offset)
	}
	var models []runnerModel
	if err := q.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}
	out := make([]*store.Runner, 0, len(models))
	for i := range models {
		out = append(out, modelToRunner(&models[i]))
	}
	return out, nil
}

func (s *Store) CountRunners(ctx context.Context, f store.RunnerFilter) (int64, error) {
	q := s.db.WithContext(ctx).Model(&runnerModel{})
	if f.TenantID != nil {
		q = q.Where("tenant_id = ?", *f.TenantID)
	}
	if f.Status != nil {
		q = q.Where("status = ?", string(*f.Status))
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count runners: %w", err)
	}
	return count, nil
}

func (s *Store) DeleteRunner(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Delete(&runnerModel{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("delete runner %s: %w", id, result.Error)
	}
	return nil
}

func (s *Store) AcquireIdempotencyLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	// Clean up expired locks lazily (non-blocking; errors tolerated).
	_ = s.db.WithContext(ctx).Delete(&idempotencyLock{}, "expires_at < ?", time.Now()).Error

	lock := idempotencyLock{
		LockKey:   key,
		ExpiresAt: time.Now().Add(ttl),
	}
	result := s.db.WithContext(ctx).Create(&lock)
	if result.Error != nil {
		if isDuplicateError(result.Error) {
			return false, nil // already locked — idempotent duplicate
		}
		return false, fmt.Errorf("acquire lock %q: %w", key, result.Error)
	}
	return true, nil
}

func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// --- Conversion helpers ---

func tenantToModel(t *store.Tenant) *tenantModel {
	labelsJSON, _ := json.Marshal(t.RunnerLabels)
	return &tenantModel{
		ID:               t.ID,
		Name:             t.Name,
		GitHubAppID:      t.GitHubAppID,
		GitHubInstallID:  t.GitHubInstallID,
		GitHubOrgName:    t.GitHubOrgName,
		WebhookSecret:    t.WebhookSecret,
		OCICompartmentID: t.OCICompartmentID,
		OCISubnetID:      t.OCISubnetID,
		MaxRunners:       t.MaxRunners,
		MinWarmPool:      t.MinWarmPool,
		IdleTimeoutSec:   t.IdleTimeoutSec,
		RunnerLabelsJSON: string(labelsJSON),
		RunnerShape:      t.RunnerShape,
		RunnerOCPUs:      t.RunnerOCPUs,
		RunnerMemoryGB:   t.RunnerMemoryGB,
		ProvisionerType:  t.ProvisionerType,
		Status:           string(t.Status),
		CreatedAt:        t.CreatedAt,
		UpdatedAt:        t.UpdatedAt,
	}
}

func modelToTenant(m *tenantModel) *store.Tenant {
	var labels []string
	_ = json.Unmarshal([]byte(m.RunnerLabelsJSON), &labels)
	return &store.Tenant{
		ID:               m.ID,
		Name:             m.Name,
		GitHubAppID:      m.GitHubAppID,
		GitHubInstallID:  m.GitHubInstallID,
		GitHubOrgName:    m.GitHubOrgName,
		WebhookSecret:    m.WebhookSecret,
		OCICompartmentID: m.OCICompartmentID,
		OCISubnetID:      m.OCISubnetID,
		MaxRunners:       m.MaxRunners,
		MinWarmPool:      m.MinWarmPool,
		IdleTimeoutSec:   m.IdleTimeoutSec,
		RunnerLabels:     labels,
		RunnerShape:      m.RunnerShape,
		RunnerOCPUs:      m.RunnerOCPUs,
		RunnerMemoryGB:   m.RunnerMemoryGB,
		ProvisionerType:  m.ProvisionerType,
		Status:           store.TenantStatus(m.Status),
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

func runnerToModel(r *store.Runner) *runnerModel {
	labelsJSON, _ := json.Marshal(r.Labels)
	return &runnerModel{
		ID:              r.ID,
		TenantID:        r.TenantID,
		JobID:           r.JobID,
		GitHubRunnerID:  r.GitHubRunnerID,
		OCIInstanceID:   r.OCIInstanceID,
		ProvisionerType: r.ProvisionerType,
		Status:          string(r.Status),
		LabelsJSON:      string(labelsJSON),
		ProvisionedAt:   r.ProvisionedAt,
		RegisteredAt:    r.RegisteredAt,
		IdleSince:       r.IdleSince,
		TerminatedAt:    r.TerminatedAt,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

func modelToRunner(m *runnerModel) *store.Runner {
	var labels []string
	_ = json.Unmarshal([]byte(m.LabelsJSON), &labels)
	return &store.Runner{
		ID:              m.ID,
		TenantID:        m.TenantID,
		JobID:           m.JobID,
		GitHubRunnerID:  m.GitHubRunnerID,
		OCIInstanceID:   m.OCIInstanceID,
		ProvisionerType: m.ProvisionerType,
		Status:          store.RunnerStatus(m.Status),
		Labels:          labels,
		ProvisionedAt:   m.ProvisionedAt,
		RegisteredAt:    m.RegisteredAt,
		IdleSince:       m.IdleSince,
		TerminatedAt:    m.TerminatedAt,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	// MySQL error 1062 = Duplicate entry
	return errors.Is(err, gorm.ErrDuplicatedKey)
}
