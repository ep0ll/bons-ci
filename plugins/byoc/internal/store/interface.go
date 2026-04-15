// Package store defines the persistence port (interface) for the BYOC platform.
// Concrete adapters (e.g., MySQL) implement this interface without being
// imported by the domain — maintaining hexagonal architecture boundaries.
package store

import (
	"context"
	"time"
)

// TenantStatus represents the operational state of a tenant account.
type TenantStatus string

const (
	TenantStatusActive      TenantStatus = "active"
	TenantStatusSuspended   TenantStatus = "suspended"
	TenantStatusOffboarding TenantStatus = "offboarding"
)

// Tenant is the canonical tenant domain model stored in the database.
type Tenant struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	GitHubAppID      int64        `json:"github_app_id"`
	GitHubInstallID  int64        `json:"github_install_id"`
	GitHubOrgName    string       `json:"github_org_name"`
	WebhookSecret    string       `json:"-"` // never serialised to API responses
	OCICompartmentID string       `json:"oci_compartment_id"`
	OCISubnetID      string       `json:"oci_subnet_id"`
	MaxRunners       int          `json:"max_runners"`
	MinWarmPool      int          `json:"min_warm_pool"`
	IdleTimeoutSec   int          `json:"idle_timeout_sec"`
	RunnerLabels     []string     `json:"runner_labels"`
	RunnerShape      string       `json:"runner_shape"`
	RunnerOCPUs      float32      `json:"runner_ocpus"`
	RunnerMemoryGB   float32      `json:"runner_memory_gb"`
	ProvisionerType  string       `json:"provisioner_type"` // "compute" | "container"
	Status           TenantStatus `json:"status"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// RunnerStatus represents the lifecycle state of a runner instance.
type RunnerStatus string

const (
	RunnerStatusProvisioning RunnerStatus = "provisioning"
	RunnerStatusRegistering  RunnerStatus = "registering"
	RunnerStatusIdle         RunnerStatus = "idle"
	RunnerStatusBusy         RunnerStatus = "busy"
	RunnerStatusTerminating  RunnerStatus = "terminating"
	RunnerStatusTerminated   RunnerStatus = "terminated"
)

// Runner is the canonical runner domain model.
type Runner struct {
	ID              string       `json:"id"`
	TenantID        string       `json:"tenant_id"`
	JobID           int64        `json:"job_id"`   // 0 if warm-pool runner
	GitHubRunnerID  int64        `json:"github_runner_id"`
	OCIInstanceID   string       `json:"oci_instance_id"`
	ProvisionerType string       `json:"provisioner_type"`
	Status          RunnerStatus `json:"status"`
	Labels          []string     `json:"labels"`
	ProvisionedAt   time.Time    `json:"provisioned_at"`
	RegisteredAt    *time.Time   `json:"registered_at,omitempty"`
	IdleSince       *time.Time   `json:"idle_since,omitempty"`
	TerminatedAt    *time.Time   `json:"terminated_at,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// TenantFilter holds optional predicates for listing tenants.
type TenantFilter struct {
	Status *TenantStatus
	Limit  int
	Offset int
}

// RunnerFilter holds optional predicates for listing runners.
type RunnerFilter struct {
	TenantID *string
	Status   *RunnerStatus
	Limit    int
	Offset   int
}

// RunnerUpdateOpts contains optional fields to update alongside the status.
type RunnerUpdateOpts struct {
	GitHubRunnerID *int64
	OCIInstanceID  *string
	RegisteredAt   *time.Time
	IdleSince      *time.Time
	TerminatedAt   *time.Time
}

// Store is the persistence port. All domain packages depend on this interface;
// no domain package imports the MySQL adapter.
type Store interface {
	// --- Tenant operations ---

	// CreateTenant persists a new tenant. Returns ErrConflict if the ID already exists.
	CreateTenant(ctx context.Context, t *Tenant) error

	// GetTenant retrieves a tenant by ID. Returns ErrNotFound if absent.
	GetTenant(ctx context.Context, id string) (*Tenant, error)

	// UpdateTenant overwrites all mutable fields. Returns ErrNotFound if absent.
	UpdateTenant(ctx context.Context, t *Tenant) error

	// ListTenants returns tenants matching the filter.
	ListTenants(ctx context.Context, f TenantFilter) ([]*Tenant, error)

	// --- Runner operations ---

	// CreateRunner persists a new runner record. Returns ErrConflict on duplicate ID.
	CreateRunner(ctx context.Context, r *Runner) error

	// GetRunner retrieves a runner by ID. Returns ErrNotFound if absent.
	GetRunner(ctx context.Context, id string) (*Runner, error)

	// UpdateRunnerStatus transitions a runner's status and optionally updates
	// metadata fields via opts. Returns ErrNotFound or ErrInvalidTransition.
	UpdateRunnerStatus(ctx context.Context, id string, status RunnerStatus, opts RunnerUpdateOpts) error

	// ListRunners returns runners matching the filter.
	ListRunners(ctx context.Context, f RunnerFilter) ([]*Runner, error)

	// CountRunners returns the count of runners matching the filter.
	CountRunners(ctx context.Context, f RunnerFilter) (int64, error)

	// DeleteRunner hard-deletes a runner record (used only by reconciler for orphans).
	DeleteRunner(ctx context.Context, id string) error

	// --- Idempotency ---

	// AcquireIdempotencyLock attempts to insert a lock row for key.
	// Returns acquired=true if the insertion succeeded (first caller).
	// Returns acquired=false if the key already exists (duplicate).
	// The lock row expires after ttl; the implementation cleans up expired rows lazily.
	AcquireIdempotencyLock(ctx context.Context, key string, ttl time.Duration) (bool, error)

	// --- Health ---

	// Ping verifies the database connection is alive.
	Ping(ctx context.Context) error
}

// Sentinel errors returned by Store implementations.
var (
	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = &storeError{code: "NOT_FOUND", msg: "resource not found"}
	// ErrConflict is returned on unique constraint violations.
	ErrConflict = &storeError{code: "CONFLICT", msg: "resource already exists"}
	// ErrInvalidTransition is returned when a state machine transition is illegal.
	ErrInvalidTransition = &storeError{code: "INVALID_TRANSITION", msg: "invalid runner status transition"}
)

type storeError struct {
	code string
	msg  string
}

func (e *storeError) Error() string { return e.msg }

// IsNotFound reports whether err is (or wraps) ErrNotFound.
func IsNotFound(err error) bool { return isStoreErr(err, ErrNotFound) }

// IsConflict reports whether err is (or wraps) ErrConflict.
func IsConflict(err error) bool { return isStoreErr(err, ErrConflict) }

func isStoreErr(err, target error) bool {
	if err == nil {
		return false
	}
	if err == target {
		return true
	}
	// unwrap one level for %w wrapped errors
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return isStoreErr(u.Unwrap(), target)
	}
	return false
}
