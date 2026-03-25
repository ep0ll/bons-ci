package resources

// controller.go – pluggable cgroup v2 controller abstraction.
//
// A Controller is a self-contained reader for one cgroupv2 subsystem
// (cpu, memory, io, pids, …).  The Registry holds a set of Controllers
// and delegates collection to all of them in one Collect() call.
//
// Design goals:
//   - Open/Closed: new controllers can be registered without touching
//     existing code.  The registry drives collection; the walker drives nothing.
//   - Dependency inversion: the Monitor depends on the Controller interface,
//     not on concrete cpu/memory/io/pids types.
//   - Testability: controllers can be replaced with test doubles.
//
// Concurrency: Registry.Collect is called from a single goroutine (the
// sampler), so no synchronisation is required inside individual controllers.
// The registry itself is built once (in init or NewMonitor) and thereafter
// read-only.

import (
	"context"
	"path/filepath"
	"time"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
)

// ─── Controller interface ─────────────────────────────────────────────────────

// Controller is implemented by each cgroupv2 subsystem reader.
// Implementations must:
//   - Return (nil, nil) when the controller's files are absent or unsupported.
//   - Not perform any I/O outside of Collect().
//   - Be safe for concurrent reads (Collect may be called from multiple
//     goroutines if the sampler ever runs in parallel — currently it does not,
//     but the interface must not preclude it).
type Controller interface {
	// Name returns the cgroupv2 controller name used in cgroup.controllers
	// (e.g. "cpu", "memory", "io", "pids"). Used only for logging/debugging.
	Name() string

	// Collect reads the controller's pseudo-files under cgroupPath and populates
	// the corresponding fields in dst. cgroupPath is an absolute path such as
	// "/sys/fs/cgroup/buildkit/abc123". dst is always a non-nil *Sample.
	//
	// Implementations must fill exactly the fields they own; they must not
	// zero out fields populated by other controllers.
	Collect(ctx context.Context, cgroupPath string, dst *resourcestypes.Sample) error
}

// ─── Registry ─────────────────────────────────────────────────────────────────

// Registry holds an ordered slice of Controllers and drives the collection
// of a complete Sample.
//
// The zero value is not useful; build a Registry with NewRegistry().
type Registry struct {
	controllers []Controller
}

// NewRegistry returns a Registry populated with the built-in set of
// cgroupv2 controllers (cpu, memory, io, pids).  Additional controllers
// can be added with Register() before the Monitor starts.
func NewRegistry() *Registry {
	r := &Registry{}
	// Registration order determines field-fill order inside Collect.
	// This order is semantically irrelevant but kept consistent for readability.
	r.Register(&cpuController{})
	r.Register(&memoryController{})
	r.Register(&ioController{})
	r.Register(&pidsController{})
	return r
}

// Register appends a Controller to the registry.
// Must be called before the Monitor begins sampling.
func (r *Registry) Register(c Controller) {
	r.controllers = append(r.controllers, c)
}

// Collect invokes every registered Controller's Collect method and assembles
// the results into a single *resourcestypes.Sample.
//
// Individual controller errors are non-fatal: a controller that returns an
// error is skipped; the error is returned alongside whatever partial data was
// collected so the caller can decide how to handle it.
//
// The returned sample always has its Timestamp_ set to tm.
func (r *Registry) Collect(ctx context.Context, cgroupPath string, tm time.Time) (*resourcestypes.Sample, error) {
	sample := &resourcestypes.Sample{Timestamp_: tm}

	var firstErr error
	for _, c := range r.controllers {
		if err := c.Collect(ctx, cgroupPath, sample); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Continue: collect whatever other controllers can still provide.
		}
	}
	return sample, firstErr
}

// ─── NetworkSampler interface ─────────────────────────────────────────────────

// NetworkSampler is implemented by types that can sample network counters
// from a container's network namespace.  It is separate from Controller
// because network stats require a handle into the container's netns rather
// than a cgroupfs path.
type NetworkSampler interface {
	// Sample returns the current network counter delta.
	// Implementations are responsible for computing the delta against their
	// own baseline.
	Sample() (*resourcestypes.NetworkSample, error)
}

// ─── cgroupPath helper ────────────────────────────────────────────────────────

// cgroupAbsPath returns the absolute path for a cgroup namespace under
// the cgroupv2 mount point.
func cgroupAbsPath(ns string) string {
	return filepath.Join(defaultMountpoint, ns)
}
