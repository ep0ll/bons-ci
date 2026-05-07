---
name: golang-docker-containerd
description: >
  Comprehensive Docker and containerd integration in Go: Docker Engine API (full lifecycle,
  events, stats, exec, networks, volumes), containerd gRPC client (tasks, snapshots, namespaces,
  leases, content store), OCI spec hardening (seccomp, capabilities, namespaces, mounts),
  image building with go-containerregistry, overlay filesystem internals, image layer caching,
  runtime metrics, and container security hardening. Cross-references: linux/SKILL.md,
  security/SKILL.md, performance/SKILL.md, observability/SKILL.md.
---

# Go Docker & Containerd — Complete Integration Guide

## 1. Docker Engine API — Full Client

```go
import (
    "github.com/docker/docker/client"
    "github.com/docker/docker/api/types"
    "github.com/docker/docker/api/types/container"
    "github.com/docker/docker/api/types/image"
    "github.com/docker/docker/api/types/network"
    "github.com/docker/docker/api/types/volume"
    "github.com/docker/docker/api/types/events"
    "github.com/docker/docker/api/types/filters"
    "github.com/opencontainers/image-spec/specs-go/v1"
    "github.com/docker/go-connections/nat"
)

// Production client with all options
func NewDockerClient(socketPath string) (*client.Client, error) {
    opts := []client.Opt{
        client.WithAPIVersionNegotiation(), // auto-negotiate API version
        client.WithTimeout(30 * time.Second),
    }
    if socketPath != "" {
        opts = append(opts, client.WithHost("unix://"+socketPath))
    } else {
        opts = append(opts, client.FromEnv) // DOCKER_HOST env
    }

    cli, err := client.NewClientWithOpts(opts...)
    if err != nil { return nil, fmt.Errorf("docker client: %w", err) }

    // Verify connection
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if _, err := cli.Ping(ctx); err != nil {
        cli.Close()
        return nil, fmt.Errorf("docker ping: %w", err)
    }
    return cli, nil
}
```

---

## 2. Container Lifecycle — Complete

```go
// ContainerSpec: production-hardened container configuration
type ContainerSpec struct {
    Name         string
    Image        string
    Cmd          []string
    Env          []string
    Labels       map[string]string
    User         string   // "uid:gid" — always set, never root
    WorkDir      string
    Network      string
    PortBindings map[string]string // "8080/tcp" → "0.0.0.0:8080"

    // Resource limits — always set both
    MemoryBytes  int64   // hard limit
    MemorySwap   int64   // mem+swap limit (-1 = unlimited swap)
    CPUNanos     int64   // 0.5 CPU = 500_000_000
    CPUShares    int64   // relative weight (1024 default)
    PidsLimit    int64   // max processes

    // Security
    Capabilities []string // drop ALL, add only what's needed
    ReadOnly     bool
    NoNewPrivs   bool
    SeccompProf  string   // seccomp profile path or "default"/"unconfined"
    AppArmor     string   // apparmor profile or "docker-default"

    // Volumes
    Mounts   []mount.Mount
    Tmpfs    map[string]string // path → options

    // Behavior
    AutoRemove  bool
    RestartPol  string // "no"|"always"|"unless-stopped"|"on-failure"
    MaxRetries  int    // for "on-failure"
    LogDriver   string // "json-file"|"local"|"none"|"syslog"
    LogOpts     map[string]string
}

func (m *ContainerManager) Run(ctx context.Context, spec ContainerSpec) (string, error) {
    // 1. Ensure image present
    if err := m.ensureImage(ctx, spec.Image); err != nil {
        return "", fmt.Errorf("Run.ensureImage: %w", err)
    }

    // 2. Build port bindings
    portBindings := nat.PortMap{}
    exposedPorts := nat.PortSet{}
    for containerPort, hostPort := range spec.PortBindings {
        p := nat.Port(containerPort)
        exposedPorts[p] = struct{}{}
        portBindings[p] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: hostPort}}
    }

    // 3. Container config
    cfg := &container.Config{
        Image:        spec.Image,
        Cmd:          spec.Cmd,
        Env:          spec.Env,
        Labels:       spec.Labels,
        User:         spec.User,
        WorkingDir:   spec.WorkDir,
        ExposedPorts: exposedPorts,
        // Never attach: managed async via logs API
        AttachStdout: false,
        AttachStderr: false,
    }

    // 4. Host config — security-hardened defaults
    capDrop := []string{"ALL"}
    capAdd := spec.Capabilities // only explicitly needed capabilities

    securityOpts := []string{"no-new-privileges:true"}
    if spec.SeccompProf != "" {
        securityOpts = append(securityOpts, "seccomp:"+spec.SeccompProf)
    }
    if spec.AppArmor != "" {
        securityOpts = append(securityOpts, "apparmor:"+spec.AppArmor)
    }

    logConfig := container.LogConfig{Type: "json-file"}
    if spec.LogDriver != "" {
        logConfig = container.LogConfig{Type: spec.LogDriver, Config: spec.LogOpts}
    }

    pidsLimit := int64(256)
    if spec.PidsLimit > 0 { pidsLimit = spec.PidsLimit }

    hostCfg := &container.HostConfig{
        AutoRemove:  spec.AutoRemove,
        NetworkMode: container.NetworkMode(spec.Network),
        PortBindings: portBindings,
        Resources: container.Resources{
            Memory:           spec.MemoryBytes,
            MemorySwap:       spec.MemorySwap,
            NanoCPUs:         spec.CPUNanos,
            CPUShares:        spec.CPUShares,
            PidsLimit:        &pidsLimit,
        },
        SecurityOpt:    securityOpts,
        ReadonlyRootfs: spec.ReadOnly,
        CapDrop:        capDrop,
        CapAdd:         capAdd,
        Mounts:         spec.Mounts,
        Tmpfs:          spec.Tmpfs,
        RestartPolicy: container.RestartPolicy{
            Name:              container.RestartPolicyMode(spec.RestartPol),
            MaximumRetryCount: spec.MaxRetries,
        },
        LogConfig: logConfig,
        // Always: limit log file size
        // (even if using json-file driver, cap at 10MB * 3 files)
    }

    // 5. Create
    resp, err := m.cli.ContainerCreate(ctx, cfg, hostCfg,
        &network.NetworkingConfig{}, nil, spec.Name)
    if err != nil { return "", fmt.Errorf("ContainerCreate: %w", err) }

    // 6. Start
    if err := m.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
        // Clean up created container on start failure
        m.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
        return "", fmt.Errorf("ContainerStart(%s): %w", resp.ID[:12], err)
    }

    m.log.InfoContext(ctx, "container started",
        slog.String("id", resp.ID[:12]),
        slog.String("name", spec.Name),
        slog.String("image", spec.Image))
    return resp.ID, nil
}

// GracefulStop: SIGTERM → wait → SIGKILL
func (m *ContainerManager) GracefulStop(ctx context.Context, id string, timeout time.Duration) error {
    secs := int(timeout.Seconds())
    stopOpts := container.StopOptions{Timeout: &secs}
    if err := m.cli.ContainerStop(ctx, id, stopOpts); err != nil {
        return fmt.Errorf("ContainerStop(%s): %w", id[:12], err)
    }
    return nil
}
```

---

## 3. Image Management

```go
// Pull with progress reporting
func (m *ContainerManager) PullImage(ctx context.Context, ref string, auth *registrytypes.AuthConfig) error {
    var pullOpts image.PullOptions
    if auth != nil {
        encoded, _ := json.Marshal(auth)
        pullOpts.RegistryAuth = base64.URLEncoding.EncodeToString(encoded)
    }

    out, err := m.cli.ImagePull(ctx, ref, pullOpts)
    if err != nil { return fmt.Errorf("ImagePull(%s): %w", ref, err) }
    defer out.Close()

    // Stream pull progress to logger
    dec := json.NewDecoder(out)
    for dec.More() {
        var event struct {
            Status   string `json:"status"`
            Progress string `json:"progress"`
            Error    string `json:"error"`
        }
        if err := dec.Decode(&event); err != nil { break }
        if event.Error != "" {
            return fmt.Errorf("pull error: %s", event.Error)
        }
        m.log.DebugContext(ctx, "pull progress",
            slog.String("status", event.Status),
            slog.String("progress", event.Progress))
    }
    return nil
}

// Image present check (avoid unnecessary pulls)
func (m *ContainerManager) ImagePresent(ctx context.Context, ref string) (bool, error) {
    _, _, err := m.cli.ImageInspectWithRaw(ctx, ref)
    if err == nil { return true, nil }
    if client.IsErrNotFound(err) { return false, nil }
    return false, fmt.Errorf("ImageInspect(%s): %w", ref, err)
}

// Build image from Dockerfile bytes
func (m *ContainerManager) BuildImage(ctx context.Context, contextTar io.Reader, tags []string) error {
    resp, err := m.cli.ImageBuild(ctx, contextTar, types.ImageBuildOptions{
        Tags:        tags,
        Dockerfile:  "Dockerfile",
        Remove:      true,       // remove intermediate containers
        ForceRemove: true,       // even on error
        PullParent:  true,       // always pull base image
        NoCache:     false,
        BuildArgs:   map[string]*string{},
        Platform:    "linux/amd64",
        Version:     types.BuilderBuildKit,  // use BuildKit
    })
    if err != nil { return fmt.Errorf("ImageBuild: %w", err) }
    defer resp.Body.Close()

    // Parse build output (check for errors)
    dec := json.NewDecoder(resp.Body)
    for dec.More() {
        var event struct {
            Stream string `json:"stream"`
            Error  string `json:"error"`
        }
        if err := dec.Decode(&event); err != nil { break }
        if event.Error != "" { return fmt.Errorf("build error: %s", event.Error) }
        if event.Stream != "" {
            m.log.DebugContext(ctx, "build", slog.String("output", event.Stream))
        }
    }
    return nil
}
```

---

## 4. Container Stats & Exec

```go
// Stream real-time container stats (CPU, memory, network, block I/O)
func (m *ContainerManager) StreamStats(ctx context.Context, id string) (<-chan ContainerStats, error) {
    statsResp, err := m.cli.ContainerStats(ctx, id, true) // true = stream
    if err != nil { return nil, fmt.Errorf("ContainerStats: %w", err) }

    ch := make(chan ContainerStats, 1)
    go func() {
        defer close(ch)
        defer statsResp.Body.Close()
        dec := json.NewDecoder(statsResp.Body)
        for dec.More() {
            var raw types.StatsJSON
            if err := dec.Decode(&raw); err != nil { return }
            select {
            case ch <- parseStats(raw):
            case <-ctx.Done(): return
            }
        }
    }()
    return ch, nil
}

type ContainerStats struct {
    CPUPercent    float64
    MemoryUsage   uint64
    MemoryLimit   uint64
    NetworkRxBytes uint64
    NetworkTxBytes uint64
    BlockRead     uint64
    BlockWrite    uint64
}

func parseStats(s types.StatsJSON) ContainerStats {
    // CPU percent calculation
    cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage)
    sysDelta := float64(s.CPUStats.SystemUsage - s.PreCPUStats.SystemUsage)
    cpuPct := 0.0
    if sysDelta > 0 {
        cpuPct = (cpuDelta / sysDelta) * float64(len(s.CPUStats.CPUUsage.PercpuUsage)) * 100.0
    }

    // Network I/O
    var rxBytes, txBytes uint64
    for _, v := range s.Networks { rxBytes += v.RxBytes; txBytes += v.TxBytes }

    // Block I/O
    var blkRead, blkWrite uint64
    for _, b := range s.BlkioStats.IoServiceBytesRecursive {
        switch b.Op { case "Read": blkRead += b.Value; case "Write": blkWrite += b.Value }
    }

    return ContainerStats{
        CPUPercent:    cpuPct,
        MemoryUsage:   s.MemoryStats.Usage,
        MemoryLimit:   s.MemoryStats.Limit,
        NetworkRxBytes: rxBytes,
        NetworkTxBytes: txBytes,
        BlockRead:     blkRead,
        BlockWrite:    blkWrite,
    }
}

// Execute command in running container
func (m *ContainerManager) Exec(ctx context.Context, id string, cmd []string) (string, int, error) {
    exec, err := m.cli.ContainerExecCreate(ctx, id, types.ExecConfig{
        Cmd:          cmd,
        AttachStdout: true,
        AttachStderr: true,
    })
    if err != nil { return "", -1, fmt.Errorf("ExecCreate: %w", err) }

    resp, err := m.cli.ContainerExecAttach(ctx, exec.ID, types.ExecStartCheck{})
    if err != nil { return "", -1, fmt.Errorf("ExecAttach: %w", err) }
    defer resp.Close()

    var stdout, stderr bytes.Buffer
    if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Reader); err != nil {
        return "", -1, fmt.Errorf("exec copy: %w", err)
    }

    inspect, err := m.cli.ContainerExecInspect(ctx, exec.ID)
    if err != nil { return "", -1, err }

    output := stdout.String()
    if stderr.Len() > 0 { output += "\nSTDERR: " + stderr.String() }
    return output, inspect.ExitCode, nil
}
```

---

## 5. Event Streaming (Reactive Pattern)

```go
// Stream Docker events with automatic reconnection
func (m *ContainerManager) StreamEvents(ctx context.Context, handler func(events.Message)) error {
    f := filters.NewArgs(
        filters.Arg("type", "container"),
        filters.Arg("event", "start"),
        filters.Arg("event", "die"),
        filters.Arg("event", "oom"),
        filters.Arg("event", "health_status"),
        filters.Arg("event", "exec_die"),
    )

    for {
        msgs, errs := m.cli.Events(ctx, events.ListOptions{Filters: f})
        for {
            select {
            case msg, ok := <-msgs:
                if !ok { goto reconnect }
                handler(msg)
            case err, ok := <-errs:
                if !ok { goto reconnect }
                if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
                    return ctx.Err()
                }
                m.log.WarnContext(ctx, "docker events error", "err", err)
                goto reconnect
            case <-ctx.Done():
                return nil
            }
        }
    reconnect:
        select {
        case <-ctx.Done(): return nil
        case <-time.After(2 * time.Second): // reconnect delay
        }
    }
}
```

---

## 6. containerd Client — Complete

```go
import (
    "github.com/containerd/containerd"
    "github.com/containerd/containerd/cio"
    "github.com/containerd/containerd/namespaces"
    "github.com/containerd/containerd/oci"
    "github.com/containerd/containerd/leases"
    "github.com/containerd/containerd/content"
    "github.com/containerd/containerd/snapshots"
    "github.com/opencontainers/runtime-spec/specs-go"
)

const ContainerdNamespace = "myapp" // logical isolation within containerd

func NewContainerdClient(ctx context.Context, socket string) (*containerd.Client, error) {
    cli, err := containerd.New(socket,
        containerd.WithDefaultNamespace(ContainerdNamespace),
        containerd.WithTimeout(10*time.Second),
    )
    if err != nil { return nil, fmt.Errorf("containerd.New: %w", err) }

    // Verify connection
    if _, err := cli.Version(ctx); err != nil {
        cli.Close()
        return nil, fmt.Errorf("containerd ping: %w", err)
    }
    return cli, nil
}

// Run container with containerd — full lifecycle
func RunContainerd(ctx context.Context, cli *containerd.Client, imageRef, id string) error {
    ctx = namespaces.WithNamespace(ctx, ContainerdNamespace)

    // 1. Create lease (prevents GC of in-progress resources)
    ctx, done, err := cli.WithLease(ctx)
    if err != nil { return fmt.Errorf("WithLease: %w", err) }
    defer done(ctx)

    // 2. Pull image (idempotent)
    img, err := cli.Pull(ctx, imageRef,
        containerd.WithPullUnpack,
        containerd.WithPullSnapshotter("overlayfs"),
    )
    if err != nil { return fmt.Errorf("Pull(%s): %w", imageRef, err) }

    // 3. Build hardened OCI spec
    spec, err := oci.GenerateSpec(ctx, cli, nil,
        oci.WithImageConfig(img),
        oci.WithNoNewPrivileges,
        oci.WithDroppedCapabilities,
        oci.WithDefaultPathEnv,
        // Mask sensitive /proc paths
        oci.WithMaskedPaths([]string{
            "/proc/acpi", "/proc/asound", "/proc/kcore",
            "/proc/keys", "/proc/latency_stats", "/proc/timer_list",
            "/proc/timer_stats", "/proc/sched_debug", "/proc/scsi",
            "/sys/firmware", "/sys/devices/virtual/powercap",
        }),
        // Read-only /proc paths
        oci.WithReadonlyPaths([]string{
            "/proc/bus", "/proc/fs", "/proc/irq",
            "/proc/sys", "/proc/sysrq-trigger",
        }),
        // Resource limits
        oci.WithMemoryLimit(256*1024*1024),  // 256MB
        oci.WithCPUCount(1),
    )
    if err != nil { return fmt.Errorf("GenerateSpec: %w", err) }

    // 4. Create container with snapshot
    ctr, err := cli.NewContainer(ctx, id,
        containerd.WithImage(img),
        containerd.WithNewSnapshot(id+"-snap", img),
        containerd.WithNewSpec(spec),
        containerd.WithContainerLabels(map[string]string{
            "app": "myapp",
        }),
    )
    if err != nil { return fmt.Errorf("NewContainer: %w", err) }
    defer ctr.Delete(ctx, containerd.WithSnapshotCleanup)

    // 5. Create task (running process)
    task, err := ctr.NewTask(ctx, cio.NewCreator(cio.WithStdio))
    if err != nil { return fmt.Errorf("NewTask: %w", err) }
    defer task.Delete(ctx)

    // 6. Wait before starting (capture exit status)
    exitCh, err := task.Wait(ctx)
    if err != nil { return fmt.Errorf("task.Wait: %w", err) }

    // 7. Start
    if err := task.Start(ctx); err != nil { return fmt.Errorf("task.Start: %w", err) }

    // 8. Wait for completion or cancellation
    select {
    case status := <-exitCh:
        if code := status.ExitCode(); code != 0 {
            return fmt.Errorf("container exited with code %d", code)
        }
        return nil
    case <-ctx.Done():
        // Graceful shutdown: SIGTERM → wait → SIGKILL
        task.Kill(ctx, unix.SIGTERM)
        select {
        case <-exitCh:
        case <-time.After(10 * time.Second):
            task.Kill(ctx, unix.SIGKILL)
            <-exitCh
        }
        return ctx.Err()
    }
}
```

---

## 7. OCI Image Building (go-containerregistry)

```go
import (
    "github.com/google/go-containerregistry/pkg/v1/empty"
    "github.com/google/go-containerregistry/pkg/v1/mutate"
    "github.com/google/go-containerregistry/pkg/v1/tarball"
    "github.com/google/go-containerregistry/pkg/name"
    "github.com/google/go-containerregistry/pkg/v1/remote"
    "github.com/google/go-containerregistry/pkg/authn"
)

// Build minimal OCI image programmatically (no Dockerfile, no Docker daemon)
func BuildOCIImage(ctx context.Context, binaryPath, imageRef string) error {
    // 1. Start from empty base
    img := empty.Image

    // 2. Add binary layer
    layer, err := tarball.LayerFromFile(binaryPath,
        tarball.WithCompression(compression.GZip),
    )
    if err != nil { return fmt.Errorf("layer: %w", err) }

    img, err = mutate.AppendLayers(img, layer)
    if err != nil { return fmt.Errorf("append layer: %w", err) }

    // 3. Set image config (metadata)
    cfg, err := img.ConfigFile()
    if err != nil { return err }

    cfg.Config.Entrypoint = []string{"/server"}
    cfg.Config.User = "65532:65532" // nonroot UID
    cfg.Config.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
    cfg.Config.Labels = map[string]string{
        "org.opencontainers.image.created": time.Now().UTC().Format(time.RFC3339),
        "org.opencontainers.image.source":  "https://github.com/org/repo",
        "org.opencontainers.image.version": os.Getenv("VERSION"),
    }
    // Reproducible builds: set creation time to epoch
    cfg.Created = v1.Time{Time: time.Unix(0, 0)}

    img, err = mutate.ConfigFile(img, cfg)
    if err != nil { return err }

    // 4. Push to registry
    ref, err := name.ParseReference(imageRef)
    if err != nil { return fmt.Errorf("parse ref: %w", err) }

    return remote.Write(ref, img,
        remote.WithContext(ctx),
        remote.WithAuthFromKeychain(authn.DefaultKeychain),
    )
}
```

---

## 8. Multi-Stage Dockerfile — Production Template

```dockerfile
# syntax=docker/dockerfile:1.7   ← enables BuildKit features

# ── Stage 1: module cache ──────────────────────────────────────────
FROM golang:1.22-alpine AS deps
WORKDIR /build
COPY go.mod go.sum ./
# Mount cache: reused across builds (BuildKit)
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download && go mod verify

# ── Stage 2: build ────────────────────────────────────────────────
FROM deps AS builder
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
# Build flags:
#   -trimpath: remove local paths (reproducibility + smaller binary)
#   -ldflags="-s -w": strip debug info + symbol table (50% size reduction)
#   -extldflags=-static: fully static binary (no libc dependency)
#   CGO_ENABLED=0: disable cgo
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -trimpath \
    -ldflags="-s -w -extldflags=-static \
              -X main.version=${VERSION} \
              -X main.commit=${COMMIT} \
              -X main.buildDate=${BUILD_DATE}" \
    -o /app/server \
    ./cmd/server

# Run tests in build (optional — can run in CI instead)
# RUN CGO_ENABLED=0 go test ./...

# ── Stage 3: certificates (for TLS) ───────────────────────────────
FROM alpine:3.19 AS certs
RUN apk add --no-cache ca-certificates tzdata

# ── Stage 4: final — distroless (no shell, no package manager) ────
FROM gcr.io/distroless/static-debian12:nonroot AS final

# Copy binary and certs only
COPY --from=builder /app/server /server
COPY --from=certs   /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs   /usr/share/zoneinfo /usr/share/zoneinfo

# OCI labels
LABEL org.opencontainers.image.title="my-service"
LABEL org.opencontainers.image.url="https://github.com/org/repo"
LABEL org.opencontainers.image.documentation="https://docs.org/my-service"

# Security: nonroot (uid 65532 in distroless:nonroot)
USER nonroot:nonroot

# Expose application and metrics ports
EXPOSE 8080 9090

# Use exec form (not shell form) — signals go to server directly
ENTRYPOINT ["/server"]

# .dockerignore — exclude everything except what's needed
# .git
# **/*_test.go
# **/*.md
# docs/
# tests/
# .env*
# Makefile
```

---

## 9. Overlay Filesystem Internals

```go
// Understanding overlay FS for container performance optimization

// Overlay FS layers:
// lowerdir: read-only base layers (image)
// upperdir: read-write container changes
// workdir:  overlay working directory (same FS as upperdir)
// merged:   unified view

// Performance implications:
// - First write to any file triggers copy-up from lowerdir to upperdir
// - Large files: copy-up is slow (e.g., writing to a 500MB log file)
// - Fix: mount large-write paths as tmpfs or bind mounts (not overlay)

// Mount with Go (for container runtime implementation)
func mountOverlay(lowerDirs []string, upperDir, workDir, mergedDir string) error {
    lower := strings.Join(lowerDirs, ":")
    opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upperDir, workDir)
    return unix.Mount("overlay", mergedDir, "overlay",
        unix.MS_NOSUID|unix.MS_NODEV, opts)
}

// Performance: tmpfs for high-write paths
func mountTmpfs(target string, sizeBytes int64) error {
    opts := fmt.Sprintf("size=%d,mode=1777", sizeBytes)
    return unix.Mount("tmpfs", target, "tmpfs",
        unix.MS_NOSUID|unix.MS_NODEV, opts)
}
```

---

## Docker/Containerd Checklist

```
IMAGE:
  □ Multi-stage build: build stage → final distroless/scratch stage
  □ CGO_ENABLED=0 + -trimpath + -ldflags="-s -w" on all binaries
  □ go.mod copied before source (layer cache optimization)
  □ BuildKit enabled (DOCKER_BUILDKIT=1 / syntax=docker/dockerfile:1)
  □ .dockerignore excludes tests, docs, .git

SECURITY:
  □ Runs as nonroot (USER nonroot:nonroot or explicit UID:GID)
  □ no-new-privileges:true in security opts
  □ All capabilities dropped; only required ones added
  □ Read-only root filesystem (ReadonlyRootfs: true)
  □ Seccomp profile applied (docker-default or custom allowlist)
  □ AppArmor profile applied (docker-default or custom)

RESOURCES:
  □ Memory limit AND memory.swap.max set (disable swap in containers)
  □ CPU limit set (NanoCPUs or CPUShares)
  □ PID limit set (prevent fork bombs)
  □ GOMAXPROCS from cgroup CPU quota (automaxprocs)
  □ GOMEMLIMIT from cgroup memory limit (~80%)

OPERATIONS:
  □ Events streamed (OOM, die events trigger alerts)
  □ Stats collected (CPU%, memory, network, block I/O)
  □ Health check defined on long-running containers
  □ Graceful stop: SIGTERM → drain → SIGKILL (terminationGracePeriod)
  □ Log driver configured with size limits
  □ Container labels set (app, version, commit for tracing)

CONTAINERD:
  □ Namespace set (logical isolation)
  □ Lease acquired before pull/create (prevents GC)
  □ Snapshots cleaned up on container delete (WithSnapshotCleanup)
  □ OCI spec: masked paths, read-only paths, dropped capabilities
```
