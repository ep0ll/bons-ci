package state

import (
	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── StateOption functional composition ───────────────────────────────────────

// StateOption is a function that transforms a State into a new State.
// Multiple StateOptions can be composed with With() to apply them in sequence.
//
// This pattern is the primary extension mechanism for external systems:
// a cache layer, a tracing layer, or a build-system integration can define
// its own StateOptions and apply them to any State without modifying the core.
type StateOption func(State) State

// With applies a sequence of StateOptions to this State, returning the result.
// Each option is applied in order; the output of one becomes the input of the next.
func (s State) With(opts ...StateOption) State {
	for _, o := range opts {
		s = o(s)
	}
	return s
}

// ─── Label / annotation options ────────────────────────────────────────────────

// WithLabel returns a StateOption that attaches an arbitrary key=value label
// to the State's constraints. Labels travel with the vertex through
// serialization and are visible to solvers, UI renderers, and audit systems.
//
// Common label keys (following LLB conventions):
//   - "llb.customname"   — display name in progress UIs
//   - "llb.description"  — human-readable description for logging
//   - "source.filename"  — source file that created this vertex
//
// Labels do NOT affect the vertex's content digest — two vertices that differ
// only in labels are considered identical for caching purposes.
func WithLabel(key, value string) StateOption {
	return func(s State) State {
		m := s.ensureMeta().clone()
		if m.labels == nil {
			m.labels = make(map[string]string)
		}
		m.labels[key] = value
		return State{ref: s.ref, meta: m}
	}
}

// WithCustomName is a convenience wrapper for WithLabel("llb.customname", name).
// The custom name appears in BuildKit's progress output and UI dashboards.
func WithCustomName(name string) StateOption {
	return WithLabel("llb.customname", name)
}

// WithDescription is a convenience wrapper for WithLabel("llb.description", desc).
func WithDescription(desc string) StateOption {
	return WithLabel("llb.description", desc)
}

// Labels returns all labels attached to this State as an immutable map copy.
func (s State) Labels() map[string]string {
	m := s.ensureMeta()
	if len(m.labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(m.labels))
	for k, v := range m.labels {
		out[k] = v
	}
	return out
}

// Label returns the value of a specific label and whether it exists.
func (s State) Label(key string) (string, bool) {
	m := s.ensureMeta()
	if m.labels == nil {
		return "", false
	}
	v, ok := m.labels[key]
	return v, ok
}

// ─── Platform options ──────────────────────────────────────────────────────────

// LinuxAmd64 is a StateOption that sets the target platform to linux/amd64.
var LinuxAmd64 = WithPlatformStr("linux", "amd64", "")

// LinuxArm64 is a StateOption that sets the target platform to linux/arm64.
var LinuxArm64 = WithPlatformStr("linux", "arm64", "")

// LinuxArm is a StateOption that sets the target platform to linux/arm/v7.
var LinuxArm = WithPlatformStr("linux", "arm", "v7")

// LinuxArm64V8 is linux/arm64/v8.
var LinuxArm64V8 = WithPlatformStr("linux", "arm64", "v8")

// Windows is a StateOption that sets the target platform to windows/amd64.
var Windows = WithPlatformStr("windows", "amd64", "")

// Darwin is a StateOption that sets the target platform to darwin/amd64.
var Darwin = WithPlatformStr("darwin", "amd64", "")

// WithPlatformStr returns a StateOption that sets the target platform from
// individual OS, architecture, and variant strings. Use the typed ops.Platform
// directly via State.Platform() for compile-time safety.
func WithPlatformStr(os, arch, variant string) StateOption {
	return func(s State) State {
		return s.Platform(ops.Platform{OS: os, Architecture: arch, Variant: variant})
	}
}

// ─── Env bulk options ─────────────────────────────────────────────────────────

// WithEnvMap returns a StateOption that adds all entries from m as environment
// variables. Existing keys are overridden.
func WithEnvMap(m map[string]string) StateOption {
	return func(s State) State {
		for k, v := range m {
			s = s.AddEnv(k, v)
		}
		return s
	}
}

// WithEnvSlice returns a StateOption that parses and adds all KEY=VALUE entries.
// Entries that do not contain "=" are skipped.
func WithEnvSlice(pairs []string) StateOption {
	return func(s State) State {
		for _, pair := range pairs {
			for i, c := range pair {
				if c == '=' {
					s = s.AddEnv(pair[:i], pair[i+1:])
					break
				}
			}
		}
		return s
	}
}

// ─── Network / security options ───────────────────────────────────────────────

// WithNetworkHost is a StateOption that sets network mode to host.
var WithNetworkHost = withNetworkOpt(ops.NetModeHost)

// WithNetworkNone is a StateOption that disables networking.
var WithNetworkNone = withNetworkOpt(ops.NetModeNone)

// WithNetworkSandbox is a StateOption that uses the default sandbox network.
var WithNetworkSandbox = withNetworkOpt(ops.NetModeSandbox)

func withNetworkOpt(mode ops.NetMode) StateOption {
	return func(s State) State { return s.Network(mode) }
}

// ─── Composition helpers ──────────────────────────────────────────────────────

// Compose returns a single StateOption that applies all given options in order.
// This is useful for building reusable option sets:
//
//	goBuilder := state.Compose(
//	    state.WithLabel("lang", "go"),
//	    state.WithPlatformStr("linux", "amd64", ""),
//	    state.WithEnvMap(map[string]string{"CGO_ENABLED": "0"}),
//	)
//	s := state.Image("golang:1.22").With(goBuilder)
func Compose(opts ...StateOption) StateOption {
	return func(s State) State {
		for _, o := range opts {
			s = o(s)
		}
		return s
	}
}

// Conditional returns opt if cond is true, otherwise returns a no-op StateOption.
// Useful for feature flags and platform-specific configuration:
//
//	s = s.With(state.Conditional(enableCache, state.WithLabel("cache", "enabled")))
func Conditional(cond bool, opt StateOption) StateOption {
	if cond {
		return opt
	}
	return func(s State) State { return s }
}

// ─── Mount option helpers (for WithMount in Exec) ─────────────────────────────

// ReadonlyMount returns an ops.Mount descriptor for a read-only bind mount.
// Use with ops.WithMount in Run():
//
//	rs := s.Run(
//	    ops.WithArgs("ls", "/src"),
//	    ops.WithCwd("/"),
//	    ops.WithMount(state.ReadonlyMount("/src", srcRef)),
//	)
func ReadonlyMount(target string, src vertex.Ref) ops.Mount {
	return ops.Mount{Target: target, Source: src, Readonly: true}
}

// WritableMount returns an ops.Mount descriptor for a writable bind mount.
func WritableMount(target string, src vertex.Ref) ops.Mount {
	return ops.Mount{Target: target, Source: src, Readonly: false}
}

// ScratchMount returns an ops.Mount descriptor for a writable scratch volume.
func ScratchMount(target string) ops.Mount {
	return ops.Mount{Target: target, Source: vertex.Ref{}, Readonly: false}
}

// CacheMount returns an ops.Mount descriptor for a persistent cache volume.
// id identifies the cache across builds; sharing controls concurrent access.
func CacheMount(target, id string, sharing ops.CacheSharingMode) ops.Mount {
	return ops.Mount{
		Target:       target,
		Type:         ops.MountTypeCache,
		CacheID:      id,
		CacheSharing: sharing,
	}
}

// TmpfsMount returns an ops.Mount descriptor for an in-memory tmpfs.
// sizeBytes is the size limit (0 = unlimited).
func TmpfsMount(target string, sizeBytes int64) ops.Mount {
	return ops.Mount{
		Target:    target,
		Type:      ops.MountTypeTmpfs,
		TmpfsSize: sizeBytes,
	}
}
