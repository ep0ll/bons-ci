package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── Mount types ─────────────────────────────────────────────────────────────

// MountType describes how a mount point is backed.
type MountType int

const (
	MountTypeBind   MountType = iota // bind-mount from another vertex output
	MountTypeCache                   // persistent cache volume
	MountTypeTmpfs                   // in-memory tmpfs
	MountTypeSSH                     // SSH agent socket
	MountTypeSecret                  // secret file
)

// CacheSharingMode controls concurrent access to a cache mount.
type CacheSharingMode int

const (
	CacheSharingShared  CacheSharingMode = iota // multiple ops may read/write simultaneously
	CacheSharingPrivate                         // each op gets its own copy
	CacheSharingLocked                          // serialised exclusive access
)

// Mount describes a single filesystem mount within an exec operation.
// The zero value is invalid; use MountBuilder to construct mounts.
type Mount struct {
	// Target is the absolute path inside the container where this mount appears.
	Target string
	// Source is the vertex output providing the filesystem content.
	// Nil means "scratch" (empty tmpfs or cache vol).
	Source vertex.Ref
	// Readonly marks the mount as read-only inside the container.
	Readonly bool
	// Type is the backing technology for this mount.
	Type MountType
	// Selector restricts the mount to a path within the source filesystem.
	Selector string
	// CacheID identifies a named persistent cache volume (Type=Cache only).
	CacheID string
	// CacheSharing controls concurrent access to the cache volume.
	CacheSharing CacheSharingMode
	// TmpfsSize limits the tmpfs size in bytes (0 = unlimited).
	TmpfsSize int64
	// NoOutput marks the mount as producing no output (its result is discarded).
	NoOutput bool
}

// SSHInfo describes an SSH agent socket forwarded into the exec container.
type SSHInfo struct {
	// ID is the SSH agent key ID (empty = default agent).
	ID string
	// Target is the path inside the container where the socket appears.
	Target string
	// Mode is the socket file permissions.
	Mode int
	// UID and GID are the socket file ownership.
	UID int
	GID int
	// Optional means the exec succeeds even if this agent is unavailable.
	Optional bool
}

// SecretInfo describes a secret injected into the exec container.
type SecretInfo struct {
	// ID is the secret's logical name (used by the solver to locate it).
	ID string
	// Target is the file path inside the container where the secret appears.
	// Nil means the secret is injected as an environment variable named Env.
	Target *string
	// Env, if non-nil, names the environment variable for the secret value.
	Env *string
	// Mode is the file permissions of the mounted secret file.
	Mode int
	// UID and GID are the file ownership.
	UID int
	GID int
	// Optional means the exec succeeds even if this secret is unavailable.
	Optional bool
}

// NetMode specifies the network namespace mode for exec containers.
type NetMode int

const (
	NetModeSandbox NetMode = iota // isolated network namespace
	NetModeHost                   // share host network namespace
	NetModeNone                   // no network access
)

// SecurityMode specifies the privilege level for exec containers.
type SecurityMode int

const (
	SecurityModeSandbox  SecurityMode = iota // standard sandbox
	SecurityModeInsecure                     // full host capabilities (dangerous)
)

// ─── ExecOp ───────────────────────────────────────────────────────────────────

// ExecOp represents a command execution within a container.
// It may have multiple mounts (each backed by a vertex output or scratch)
// and may produce multiple outputs (one per writable non-cache mount).
type ExecOp struct {
	id          string
	meta        ExecMeta
	mounts      []Mount
	secrets     []SecretInfo
	ssh         []SSHInfo
	constraints Constraints
	// outputRefs maps mount target → Ref for writable output mounts.
	// Built once at construction time.
	outputRefs map[string]vertex.Ref
	// inputs caches the computed input list for Inputs().
	inputs []vertex.Vertex
}

// ExecMeta carries the execution parameters for an ExecOp.
type ExecMeta struct {
	Args           []string          `json:"args"`
	Env            []string          `json:"env"`
	Cwd            string            `json:"cwd"`
	User           string            `json:"user,omitempty"`
	Hostname       string            `json:"hostname,omitempty"`
	Network        NetMode           `json:"network,omitempty"`
	Security       SecurityMode      `json:"security,omitempty"`
	ExtraHosts     []HostIP          `json:"extra_hosts,omitempty"`
	ValidExitCodes []int             `json:"valid_exit_codes,omitempty"`
	CgroupParent   string            `json:"cgroup_parent,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

// HostIP is a custom hostname-to-IP mapping injected into /etc/hosts.
type HostIP struct {
	Host string `json:"host"`
	IP   string `json:"ip"`
}

var _ vertex.Vertex = (*ExecOp)(nil)
var _ vertex.Named = (*ExecOp)(nil)

// NewExecOp constructs an ExecOp from a root filesystem ref, execution metadata,
// and mount descriptors.
//
// The returned ExecOp is immutable. Mount outputs are accessible via MountRef.
func NewExecOp(root vertex.Ref, meta ExecMeta, mounts []Mount, c Constraints) *ExecOp {
	// Normalise mounts: root is always first, then sorted by Target.
	allMounts := make([]Mount, 0, len(mounts)+1)
	rootMount := Mount{Target: "/", Source: root, Readonly: false}
	allMounts = append(allMounts, rootMount)
	for _, m := range mounts {
		if m.Target == "/" {
			// Caller is overriding the root explicitly; replace.
			allMounts[0] = m
		} else {
			allMounts = append(allMounts, m)
		}
	}

	// Sort non-root mounts by target for determinism.
	sort.Slice(allMounts[1:], func(i, j int) bool {
		return allMounts[1:][i].Target < allMounts[1:][j].Target
	})

	e := &ExecOp{
		meta:        meta,
		mounts:      allMounts,
		constraints: c,
	}
	e.id = e.computeID()
	e.outputRefs = e.buildOutputRefs()
	e.inputs = e.buildInputs()
	return e
}

func (e *ExecOp) computeID() string {
	// The ID must include everything that makes this exec distinct:
	// meta, mounts (inputs + targets), constraints.
	type mountID struct {
		Target   string `json:"target"`
		Source   string `json:"source,omitempty"` // source vertex ID
		Readonly bool   `json:"readonly,omitempty"`
		Selector string `json:"selector,omitempty"`
		CacheID  string `json:"cache_id,omitempty"`
	}
	mIDs := make([]mountID, len(e.mounts))
	for i, m := range e.mounts {
		mid := mountID{
			Target:   m.Target,
			Readonly: m.Readonly,
			Selector: m.Selector,
			CacheID:  m.CacheID,
		}
		if !m.Source.IsZero() {
			mid.Source = fmt.Sprintf("%s:%d", m.Source.Vertex.ID(), m.Source.Index)
		}
		mIDs[i] = mid
	}
	return idOf(struct {
		Kind     string    `json:"kind"`
		Meta     ExecMeta  `json:"meta"`
		Mounts   []mountID `json:"mounts"`
		Platform *Platform `json:"platform,omitempty"`
	}{
		Kind:     string(vertex.KindExec),
		Meta:     e.meta,
		Mounts:   mIDs,
		Platform: e.constraints.Platform,
	})
}

func (e *ExecOp) buildOutputRefs() map[string]vertex.Ref {
	refs := make(map[string]vertex.Ref)
	outputIdx := 0
	for _, m := range e.mounts {
		if m.Readonly || m.Type == MountTypeCache || m.Type == MountTypeTmpfs || m.NoOutput {
			continue
		}
		refs[m.Target] = vertex.Ref{Vertex: e, Index: outputIdx}
		outputIdx++
	}
	return refs
}

func (e *ExecOp) buildInputs() []vertex.Vertex {
	seen := make(map[string]bool)
	var inputs []vertex.Vertex
	for _, m := range e.mounts {
		if m.Source.IsZero() {
			continue
		}
		id := m.Source.Vertex.ID()
		if !seen[id] {
			seen[id] = true
			inputs = append(inputs, m.Source.Vertex)
		}
	}
	return inputs
}

func (e *ExecOp) ID() string               { return e.id }
func (e *ExecOp) Kind() vertex.Kind        { return vertex.KindExec }
func (e *ExecOp) Inputs() []vertex.Vertex  { return e.inputs }
func (e *ExecOp) Meta() ExecMeta           { return e.meta }
func (e *ExecOp) Mounts() []Mount          { return e.mounts }
func (e *ExecOp) Constraints() Constraints { return e.constraints }

func (e *ExecOp) Name() string {
	if len(e.meta.Args) == 0 {
		return "exec"
	}
	return "exec: " + strings.Join(e.meta.Args, " ")
}

func (e *ExecOp) Validate(ctx context.Context) error {
	if len(e.meta.Args) == 0 {
		return fmt.Errorf("exec: at least one argument is required")
	}
	if e.meta.Cwd == "" {
		return fmt.Errorf("exec: working directory (cwd) must be set")
	}
	if len(e.mounts) == 0 {
		return fmt.Errorf("exec: at least one mount (root) is required")
	}
	for _, m := range e.mounts {
		if m.Target == "" {
			return fmt.Errorf("exec: mount target must not be empty")
		}
		if !strings.HasPrefix(m.Target, "/") {
			return fmt.Errorf("exec: mount target %q must be an absolute path", m.Target)
		}
	}
	return nil
}

// RootRef returns a Ref to the root filesystem output of this exec.
// This is the writable root mount after the command completes.
func (e *ExecOp) RootRef() vertex.Ref {
	if ref, ok := e.outputRefs["/"]; ok {
		return ref
	}
	return vertex.Ref{}
}

// MountRef returns a Ref to the output of a specific mount point.
// Returns a zero Ref if the mount is read-only, not found, or produces no output.
func (e *ExecOp) MountRef(target string) vertex.Ref {
	return e.outputRefs[target]
}

// ─── Exec builder (functional options) ───────────────────────────────────────

// ExecOption is a functional option for constructing an ExecOp.
type ExecOption func(*execBuilder)

type execBuilder struct {
	meta        ExecMeta
	mounts      []Mount
	secrets     []SecretInfo
	ssh         []SSHInfo
	constraints Constraints
}

// Exec constructs an ExecOp using functional options.
// root is the filesystem that becomes the container's root.
//
// Example:
//
//	alpine := ops.Image("alpine").Ref()
//	e := ops.Exec(alpine,
//	    ops.WithArgs("sh", "-c", "echo hello"),
//	    ops.WithCwd("/"),
//	    ops.WithEnv("FOO=bar"),
//	)
func Exec(root vertex.Ref, opts ...ExecOption) *ExecOp {
	b := &execBuilder{
		// Cwd intentionally left empty: callers must provide WithCwd explicitly.
		// Validation will reject an empty cwd.
		meta: ExecMeta{},
	}
	for _, o := range opts {
		o(b)
	}
	return NewExecOp(root, b.meta, b.mounts, b.constraints)
}

// WithArgs sets the command and arguments for the exec.
func WithArgs(args ...string) ExecOption {
	return func(b *execBuilder) { b.meta.Args = args }
}

// WithShlex parses a shell-quoted string into args.
func WithShlex(cmd string) ExecOption {
	return func(b *execBuilder) { b.meta.Args = shellSplit(cmd) }
}

// WithCwd sets the working directory inside the container.
func WithCwd(cwd string) ExecOption {
	return func(b *execBuilder) { b.meta.Cwd = cwd }
}

// WithEnv appends an environment variable in KEY=VALUE form.
func WithEnv(kv ...string) ExecOption {
	return func(b *execBuilder) { b.meta.Env = append(b.meta.Env, kv...) }
}

// WithUser sets the user for the exec (UID, username, or "user:group").
func WithUser(user string) ExecOption {
	return func(b *execBuilder) { b.meta.User = user }
}

// WithNetwork sets the network mode for the exec container.
func WithNetwork(mode NetMode) ExecOption {
	return func(b *execBuilder) { b.meta.Network = mode }
}

// WithMount adds a filesystem mount to the exec.
func WithMount(m Mount) ExecOption {
	return func(b *execBuilder) { b.mounts = append(b.mounts, m) }
}

// WithConstraints sets build constraints for the exec.
func WithConstraints(c Constraints) ExecOption {
	return func(b *execBuilder) { b.constraints = c }
}

// WithExtraHost adds a custom hostname→IP mapping to the exec container.
func WithExtraHost(host, ip string) ExecOption {
	return func(b *execBuilder) {
		b.meta.ExtraHosts = append(b.meta.ExtraHosts, HostIP{Host: host, IP: ip})
	}
}

// shellSplit splits a shell-quoted command string into words.
// This is a minimal implementation — production code should use a proper shlex library.
func shellSplit(s string) []string {
	return strings.Fields(s)
}
