// Package provisioner defines the compute provisioning port for the BYOC platform.
// Concrete adapters (OCI Compute, OCI Container Instances) implement this interface,
// enabling the autoscaler to remain infrastructure-agnostic.
package provisioner

import "context"

// Provisioner is the compute provisioning port. Implementations must be
// idempotent: calling Provision twice with the same RunnerSpec must not
// create duplicate resources — it should return the existing resource ID.
type Provisioner interface {
	// Provision creates the underlying compute resource for a runner.
	// Returns the OCI resource identifier (instance OCID or container instance OCID).
	// Cloud-init / user-data installs the GitHub Actions runner binary and
	// auto-registers it using the provided RegistrationToken.
	Provision(ctx context.Context, spec RunnerSpec) (ociResourceID string, err error)

	// Terminate destroys the compute resource. Idempotent — calling on an
	// already-terminated resource returns nil.
	Terminate(ctx context.Context, ociResourceID string) error

	// Describe returns the current OCI resource state (used by the reconciler
	// to detect orphaned runners that are no longer tracked in the DB).
	Describe(ctx context.Context, ociResourceID string) (*InstanceState, error)

	// Type returns a human-readable provisioner type string ("compute" | "container").
	// Used as a Prometheus label.
	Type() string
}

// RunnerSpec describes the desired compute resource for a single runner.
type RunnerSpec struct {
	// TenantID is the owning tenant — used for OCI freeform tag and runner name prefix.
	TenantID string
	// RunnerID is the platform-assigned UUID for this runner.
	RunnerID string
	// RegistrationToken is the short-lived GitHub runner registration token.
	// It is written into the cloud-init user-data and never persisted.
	RegistrationToken string
	// GitHubOrgName is the GitHub organisation the runner belongs to.
	GitHubOrgName string
	// CompartmentID is the OCI compartment in which to create the resource.
	CompartmentID string
	// SubnetID is the private subnet OCID in which to place the instance NIC.
	SubnetID string
	// Shape is the OCI compute shape (e.g. "VM.Standard.E4.Flex").
	Shape string
	// OCPUs is the number of OCPUs for Flex shapes.
	OCPUs float32
	// MemoryGB is the memory in GB for Flex shapes.
	MemoryGB float32
	// ImageID is the OCI custom image OCID with the runner binary pre-installed.
	ImageID string
	// Labels are applied as GitHub runner labels and as OCI freeform tags.
	Labels []string
	// Tags are additional OCI freeform tags beyond the platform defaults.
	Tags map[string]string
	// Ephemeral signals whether the runner should be configured with --ephemeral
	// (self-destructs after one job). Persistent runners omit this flag.
	Ephemeral bool
}

// InstanceState represents the observed OCI resource state.
type InstanceState struct {
	OCIResourceID string
	// State is the raw OCI lifecycle state ("RUNNING", "STOPPED", "TERMINATED", …).
	State string
	// Running is true when the OCI lifecycle state indicates the resource is operational.
	Running bool
}

// Sentinel errors for the provisioner package.
var (
	ErrProvisionFailed  = &provisionerError{code: "PROVISION_FAILED", msg: "failed to provision compute resource"}
	ErrTerminateFailed  = &provisionerError{code: "TERMINATE_FAILED", msg: "failed to terminate compute resource"}
	ErrResourceNotFound = &provisionerError{code: "RESOURCE_NOT_FOUND", msg: "OCI resource not found"}
)

type provisionerError struct {
	code string
	msg  string
}

func (e *provisionerError) Error() string { return e.msg }
