# bonnie-cicd Architecture

Deep-dive reference for contributors and operators who need to understand
how every layer of bonnie-cicd fits together.

---

## 1. LinuxKit Model

LinuxKit produces a single-purpose, immutable OS image from a declarative
YAML specification (`bonnie.yml`). The resulting image has no package manager,
no init system (only a minimal `/init`), and no persistent storage by default.

```
bonnie.yml
    │
    ├── kernel:     Linux 6.6 LTS binary + cmdline
    ├── init:       runc, containerd, CA certs (baked into initrd)
    ├── onboot:     sequential containers that run once and exit
    └── services:   long-running containers managed by containerd
```

Every `onboot` and `services` entry is an OCI container image. LinuxKit
pulls them all at build time, verifies their digests, and bakes them into
the initrd. At boot, **no network access is needed** to start any service —
everything is already in the image.

---

## 2. Boot Sequence

```
 t=0 ms    BIOS/UEFI → bootloader
           ↓
 t~100 ms  Kernel decompresses (zstd: ~3x faster than gzip)
           ↓
 t~200 ms  Kernel initialises: MMU, scheduler, devices
           ↓
 t~300 ms  initrd mounted; /init (linuxkit-init) starts
           ↓
 t~350 ms  runc spawns onboot containers sequentially:
           │
           ├─ rngd         seed CSPRNG from RDRAND + /dev/urandom mixing
           ├─ cgroupfs     mount /sys/fs/cgroup (cgroup v2)
           ├─ sysctl       apply all tunables from /etc/sysctl.d/*
           ├─ dhcpcd       DHCP on eth0; wait for lease
           ├─ mount-state  bind-mount /var/lib/linuxkit (tmpfs)
           ├─ format-state prepare containerd snapshot dirs
           ├─ metadata     cloud metadata (hostname, SSH keys)
           ├─ qemu-binfmt  register foreign CPU binfmt_misc entries
           ├─ hugepages    allocate 512 × 2 MiB huge pages upfront
           └─ modules      modprobe tcp_bbr, sch_fq
           ↓
 t~700 ms  All onboot containers exited. containerd starts services
           IN PARALLEL:
           │
           ├─ containerd        ~1.3 s to socket ready
           ├─ nydus-snapshotter ~1.8 s to socket ready (waits for containerd)
           ├─ stargz-snapshotter ~2.3 s to socket ready
           ├─ buildkitd          ~2.8 s to socket ready
           ├─ bonnie             ~3.3 s (waits for containerd + nydus + stargz)
           ├─ node-exporter      ~0.5 s to metrics-ready
           └─ getty              ready immediately (INSECURE=false → no-op)
           ↓
 t~4000 ms bonnie accepts RPCs — system READY
```

The critical path is: `containerd → nydus-snapshotter → bonnie`.
containerd must be ready before nydus can register its proxy plugin.
bonnie must see all three sockets before accepting build jobs.

---

## 3. Lazy Image Pulling: Data Path

```
CI Job Submitted
    │
    ▼
bonnie gRPC handler
    │ BONNIE_SNAPSHOTTER_PRIORITY=nydus,stargz,overlayfs
    │ Try nydus first
    ▼
containerd CreateContainer
    │ snapshotter=nydus (from proxy_plugins config)
    ▼
nydus-snapshotter PrepareSnapshot
    │
    ├── Is image in nydus format? (check manifest annotation)
    │       YES ──►  FUSE mount via nydusd
    │                    │
    │                    ├── On first file access:
    │                    │   GET chunk from registry (HTTP range request)
    │                    │   Cache chunk to /var/lib/nydus/cache/<blobhash>.blob
    │                    │   Return data via FUSE read
    │                    │
    │                    └── On subsequent access:
    │                        Read from blob cache (local disk)
    │                        No registry contact needed
    │
    └── NO ──► fall through to stargz
                    │
                    ├── Is image in eStargz/SOCI format?
                    │       YES ──► stargz FUSE mount (similar lazy-pull)
                    │
                    └── NO ──► full pull via overlayfs
                                containerd pulls all layers
                                extracts to overlayfs snapshot
                                (slow path: ~25 s for 2 GB image)
```

**Shared daemon mode** (nydus): one `nydusd` process serves ALL containers.
Its blob cache is a flat directory of `<content-hash>.blob` files. The LRU
eviction is managed by `scripts/nydus-cache-gc.sh`.

---

## 4. Containerd Snapshotter Architecture

```
containerd
    │
    ├── snapshotter: overlayfs   (built-in, default)
    │       Uses Linux overlayfs kernel module.
    │       Full pull required. No lazy access.
    │
    ├── proxy_plugin: nydus      (external gRPC proxy)
    │   → /run/containerd-nydus/containerd-nydus-grpc.sock
    │       Wraps nydusd FUSE daemon.
    │       Lazy pull via HTTP range requests.
    │       Blob cache: /var/lib/nydus/cache/
    │
    └── proxy_plugin: stargz     (external gRPC proxy)
        → /run/containerd-stargz-grpc/containerd-stargz-grpc.sock
            Wraps stargz FUSE daemon.
            Lazy pull for eStargz/SOCI images.
            Background prefetch: 16 MiB chunks.
```

bonnie selects the snapshotter per-image via the
`containerd.io/snapshot/nydus-source` annotation or by trying each in
priority order and falling through on `ErrNotSupported`.

---

## 5. Security Architecture: Defence in Depth

```
Layer 0:  Hardware
          Intel TXT / AMD SEV / ARM TrustZone (platform-dependent)
          TPM PCR 10 extended by IMA measurements

Layer 1:  Kernel
          KASLR + PTI + Retpoline + CFI + Shadow Call Stack
          Module signing (SHA-512, forced)
          Lockdown=integrity (no raw hardware write from userspace)
          seccomp BPF JIT hardening

Layer 2:  LSM Stack (lsm= ordering)
          lockdown → yama → apparmor → bpf → landlock
          Each LSM makes an independent policy decision.
          All must permit for the operation to succeed.

Layer 3:  OCI Runtime
          noNewPrivileges on every container
          Capability bounding set (no CAP_SYS_RAWIO, no CAP_NET_RAW, etc.)
          Masked /proc paths, read-only /proc/sys
          seccomp allowlist per service
          AppArmor profile per service

Layer 4:  Filesystem
          Read-only rootfs
          Writable state on tmpfs only (ephemeral)
          /tmp: noexec, nosuid, nodev
          IMA appraisal on all executables

Layer 5:  Network
          No raw socket access (no CAP_NET_RAW in bonnie/nydus)
          RP filter blocks spoofed source IPs
          SYN cookies block SYN flood
          No ICMP redirect acceptance
          Only 5 ports listening (all metrics)

Layer 6:  Supply Chain
          All images pinned by sha256 digest
          SBOM generated and attested (SLSA level 2)
          cosign keyless signature (Sigstore)
          Trivy CVE scan on every build
```

An attacker who escapes a build container faces:
- AppArmor enforcement restricting filesystem and capability access
- seccomp blocking dangerous syscalls
- noNewPrivileges preventing setuid escalation
- Kernel lockdown blocking `/dev/mem`, module loading
- Read-only rootfs preventing persistence
- IMA detecting tampered binaries on next access

---

## 6. cgroups v2 Hierarchy

```
/  (cgroup root)
└── system/
    ├── containerd/        CPUWeight=50  MemoryMax=4G
    ├── nydus/             CPUWeight=20  MemoryMax=2G
    ├── stargz/            CPUWeight=10  MemoryMax=512M
    ├── buildkit/          CPUWeight=80  MemoryMax=8G
    ├── bonnie/            CPUWeight=100 MemoryMax=16G
    │   ├── job-abc123/    per-build job cgroup
    │   ├── job-def456/
    │   └── ...
    └── shimv2/            containerd shim processes
```

Resource limits are declared in `bonnie.yml` via `runtime.cgroups`.
The `CFS_BANDWIDTH` kernel config enables CPU quota enforcement.
`MEMCG_SWAP` allows memory+swap accounting (swap is disabled so this
is redundant but ensures correctness if swap is ever re-enabled).

---

## 7. Networking Inside the Image

```
eth0 (host network)
  │ DHCP via dhcpcd (onboot)
  │ 
  ├── containerd (CRI network)
  │     Creates per-container veth pairs
  │     CNI plugins handle IP assignment
  │
  ├── nydus-snapshotter
  │     Connects to OCI registry (HTTPS/443) for chunk fetches
  │
  └── bonnie
        Listens on unix:///run/bonnie/bonnie.sock (gRPC)
        Metrics on :9091/metrics (TCP)
        No ingress from external except metrics
```

There is no inter-container networking except via unix sockets on the
host filesystem (`/run/*`). All service-to-service communication uses
unix domain sockets, not TCP, for maximum throughput and zero latency.

---

## 8. Key Files and Their Roles

| File | Role |
|------|------|
| `bonnie.yml` | Single source of truth for the entire image |
| `kernel-config-fragments/bonnie-cicd.config` | Kconfig merge fragment |
| `runtime/seccomp-bonnie.json` | syscall allowlist for all services |
| `runtime/oci-spec-patch.json` | OCI spec hardening baseline |
| `runtime/ima-policy` | Kernel IMA measurement + appraisal rules |
| `runtime/apparmor.d/*` | Per-service AppArmor profiles |
| `scripts/pin-digests.sh` | Resolves tags → digests for reproducibility |
| `scripts/benchmark-boot.sh` | Boot time regression testing |
| `scripts/verify-security.sh` | Runtime security posture checker |
| `scripts/health-check.sh` | Service health monitoring |
| `scripts/nydus-cache-gc.sh` | Blob cache garbage collection |
| `scripts/build-kernel.sh` | Custom kernel build pipeline |
| `scripts/setup-host.sh` | Host preparation for CI runners |
| `scripts/rotate-keys.sh` | Module signing key rotation |
| `monitoring/prometheus-alerts.yaml` | Alerting rules |
| `monitoring/grafana-dashboard.json` | Pre-built Grafana dashboard |
| `monitoring/prometheus-scrape-config.yaml` | Prometheus scrape config |
| `docs/tuning-guide.md` | Performance tuning reference |
| `docs/security-guide.md` | Security hardening reference |
| `docs/operations-runbook.md` | Day-2 operations procedures |
| `.github/workflows/bonnie-linuxkit.yml` | CI/CD pipeline |
| `.github/workflows/dependency-update.yml` | Weekly digest update |

---

## 9. Decision Log

### Why LinuxKit over a conventional Linux distro?

- **Immutable**: no package manager, no shell by default, rootfs is read-only.
  An attacker who escapes a container cannot install persistence.
- **Minimal**: only what is declared in `bonnie.yml` is present. No cron,
  no syslog daemon, no SSH server, no unnecessary kernel modules.
- **Reproducible**: every build from the same `bonnie.yml` with the same
  digests produces an identical image. CI flakiness from OS drift is eliminated.
- **Fast boot**: no systemd dependency resolution, no udev, no DBus.
  Boot is a linear sequence with maximum parallelism in the services phase.

### Why nydus over stargz?

nydus uses a dedicated binary chunk format with stronger compression (zstd)
and a content-addressed chunk store. The shared daemon mode means the blob
cache is pooled across all containers — critical for a CI node running many
parallel jobs that pull the same base images.

stargz is kept as a fallback for images that are not in nydus format.

### Why PREEMPT_NONE + HZ_100?

CI builds are batch workloads. They do not need sub-millisecond scheduler
responsiveness. `PREEMPT_NONE` + `HZ_100` reduces scheduler overhead by ~10x
compared to `PREEMPT_VOLUNTARY` + `HZ_1000`, directly translating to more
CPU cycles for build jobs.

### Why cgroups v2 only?

cgroups v2 provides a unified hierarchy with pressure-stall information (PSI),
better resource accounting, and a simpler mental model. cgroups v1 has
per-subsystem hierarchies that can conflict. bonnie requires v2 for its
per-job resource isolation.

### Why not systemd?

systemd's init process is heavier than LinuxKit's `/init` + runc + containerd
combination. More importantly, systemd units are mutable at runtime — an
attacker can write a unit file and reload. LinuxKit's service definitions are
baked into the image at build time and cannot be modified at runtime.
