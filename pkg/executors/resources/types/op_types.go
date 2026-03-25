package types

import "time"

// ─── Operation kind ───────────────────────────────────────────────────────────

// OperationKind classifies the type of BuildKit operation being monitored.
// It is stored alongside resource samples so dashboards and profilers can
// break down resource consumption by operation type.
type OperationKind string

const (
	// OperationExec is a container exec (RUN instruction, interactive exec).
	OperationExec OperationKind = "exec"
	// OperationPull is a registry image pull (resolver + unpack).
	OperationPull OperationKind = "pull"
	// OperationPush is a registry image push.
	OperationPush OperationKind = "push"
	// OperationDiffApply is containerd's diff.Applier applying a layer tar to a snapshot.
	OperationDiffApply OperationKind = "diff_apply"
	// OperationDiffCompare is containerd's diff.Comparer generating a layer diff.
	OperationDiffCompare OperationKind = "diff_compare"
	// OperationSnapshot is a snapshotter Prepare/Commit/Remove operation.
	OperationSnapshot OperationKind = "snapshot"
	// OperationUnpack is the full image unpack pipeline (pull + diff.Apply chain).
	OperationUnpack OperationKind = "unpack"
	// OperationCache is a BuildKit cache import/export operation.
	OperationCache OperationKind = "cache"
	// OperationUnknown is a fallback for operations that do not fit above kinds.
	OperationUnknown OperationKind = "unknown"
)

// ─── Operation metadata ───────────────────────────────────────────────────────

// OperationMeta carries identity and classification metadata for one
// BuildKit operation. It is embedded in OperationSamples so the samples
// are self-describing when serialised (e.g. written to a trace file).
type OperationMeta struct {
	// ID is an opaque identifier assigned by the caller (e.g. vertex digest,
	// request UUID, or snapshotter key). Must be unique within a Monitor lifetime.
	ID string `json:"id"`
	// Kind classifies the operation (exec, pull, push, …).
	Kind OperationKind `json:"kind"`
	// Description is a human-readable label (e.g. "RUN apt-get install -y curl").
	Description string `json:"description,omitempty"`
	// StartTime is the wall-clock time at which Start() was called.
	StartTime time.Time `json:"startTime"`
	// EndTime is the wall-clock time at which Close/CloseAsync completed.
	// Zero if the operation is still running.
	EndTime time.Time `json:"endTime,omitempty"`
}

// Duration returns the elapsed time of the operation.
// Returns zero if the operation has not yet ended.
func (m *OperationMeta) Duration() time.Duration {
	if m.EndTime.IsZero() {
		return 0
	}
	return m.EndTime.Sub(m.StartTime)
}

// ─── Operation samples ────────────────────────────────────────────────────────

// OperationSamples enriches a Samples value with the identity and timing of
// the operation that produced it. This allows post-processing tools to join
// resource data with build graph vertices without external bookkeeping.
type OperationSamples struct {
	// Meta contains identity and classification for this operation.
	Meta OperationMeta `json:"meta"`
	// Samples contains the raw time-series resource data.
	*Samples
}

// ─── Operation deltas ─────────────────────────────────────────────────────────

// OperationResourceDelta summarises the change in resource counters from
// the start of an operation to its end. It is derived from the first and
// last Sample in an OperationSamples.Samples slice and is useful for
// displaying "this build step consumed X CPU-seconds, Y MiB of I/O" in UIs.
type OperationResourceDelta struct {
	// Duration is the wall-clock time of the operation.
	Duration time.Duration `json:"duration"`
	// CPUNanos is the total CPU time consumed (user + system) in nanoseconds.
	CPUNanos uint64 `json:"cpuNanos,omitempty"`
	// ReadBytes is the total bytes read from block devices.
	ReadBytes uint64 `json:"readBytes,omitempty"`
	// WriteBytes is the total bytes written to block devices.
	WriteBytes uint64 `json:"writeBytes,omitempty"`
	// MemoryPeakBytes is the high-water-mark memory usage observed.
	MemoryPeakBytes uint64 `json:"memoryPeakBytes,omitempty"`
	// MajorFaults is the number of major page faults (disk reads) during the op.
	MajorFaults uint64 `json:"majorFaults,omitempty"`
	// OOMEvents is the number of OOM events triggered during the operation.
	OOMEvents uint64 `json:"oomEvents,omitempty"`
}

// ComputeDelta derives an OperationResourceDelta from an OperationSamples.
// It requires at least two samples; returns nil if the slice is too short.
func ComputeDelta(os *OperationSamples) *OperationResourceDelta {
	if os == nil || os.Samples == nil || len(os.Samples.Samples) < 2 {
		return nil
	}
	first := os.Samples.Samples[0]
	last := os.Samples.Samples[len(os.Samples.Samples)-1]

	d := &OperationResourceDelta{
		Duration: os.Meta.Duration(),
	}

	if first.CPUStat != nil && last.CPUStat != nil &&
		first.CPUStat.UsageNanos != nil && last.CPUStat.UsageNanos != nil &&
		*last.CPUStat.UsageNanos >= *first.CPUStat.UsageNanos {
		d.CPUNanos = *last.CPUStat.UsageNanos - *first.CPUStat.UsageNanos
	}

	if first.IOStat != nil && last.IOStat != nil {
		if first.IOStat.ReadBytes != nil && last.IOStat.ReadBytes != nil &&
			*last.IOStat.ReadBytes >= *first.IOStat.ReadBytes {
			d.ReadBytes = *last.IOStat.ReadBytes - *first.IOStat.ReadBytes
		}
		if first.IOStat.WriteBytes != nil && last.IOStat.WriteBytes != nil &&
			*last.IOStat.WriteBytes >= *first.IOStat.WriteBytes {
			d.WriteBytes = *last.IOStat.WriteBytes - *first.IOStat.WriteBytes
		}
	}

	// Peak memory: scan all samples for the maximum.
	for _, s := range os.Samples.Samples {
		if s.MemoryStat != nil && s.MemoryStat.Peak != nil {
			if *s.MemoryStat.Peak > d.MemoryPeakBytes {
				d.MemoryPeakBytes = *s.MemoryStat.Peak
			}
		}
	}

	if last.MemoryStat != nil {
		d.MajorFaults = derefUint64(last.MemoryStat.Pgmajfault) - derefUint64(first.MemoryStat.Pgmajfault)
		d.OOMEvents = last.MemoryStat.OomEvents - first.MemoryStat.OomEvents
	}

	return d
}

// derefUint64 is a nil-safe dereference helper for *uint64.
func derefUint64(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}
