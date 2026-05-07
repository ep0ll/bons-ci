# bonnie-cicd — LinuxKit Image

A **minimal, immutable, reproducible** Linux image purpose-built for CI/CD workloads
powered by the **bonnie** build system.

---

## Architecture at a glance

```
┌──────────────────────────────────────────────────────────────────┐
│  bonnie-cicd LinuxKit Image                                      │
│                                                                  │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────────┐   │
│  │  containerd │  │  buildkitd   │  │  bonnie (build sys)  │   │
│  │  (CRI + OCI)│  │  (image bld) │  │  gRPC / unix socket  │   │
│  └──────┬──────┘  └──────┬───────┘  └──────────┬───────────┘   │
│         │                │                      │               │
│  ┌──────▼──────┐  ┌──────▼───────┐  ┌──────────▼───────────┐   │
│  │  overlayfs  │  │nydus-snap.   │  │  stargz-snap.         │   │
│  │ (default)   │  │(lazy-pull,   │  │  (eStargz/SOCI        │   │
│  │             │  │ FUSE, cache) │  │   lazy-pull)          │   │
│  └─────────────┘  └──────────────┘  └───────────────────────┘   │
│                                                                  │
│  ┌──────────────┐  ┌─────────────┐  ┌───────────────────────┐   │
│  │  qemu-binfmt │  │ node-export │  │  AppArmor + seccomp   │   │
│  │  (multi-arch)│  │ er (metrics)│  │  (per-service profiles│   │
│  └──────────────┘  └─────────────┘  └───────────────────────┘   │
│                                                                  │
│  Kernel 6.6 LTS · cgroups v2 only · read-only rootfs            │
│  BBR congestion · io_uring · BPF JIT · KASLR + Retpoline        │
└──────────────────────────────────────────────────────────────────┘
```

---

## Prerequisites

| Tool | Minimum version | Purpose |
|------|----------------|---------|
| `linuxkit` | v1.2.0 | Image assembly |
| `docker` | 24.x | Pull images during build |
| `qemu-system-x86_64` | 8.x | Smoke-test boot |
| `python3` + `pyyaml` | 3.11 / any | YAML lint |
| `cosign` | 2.x | Image signing (CI only) |

```bash
# macOS
brew install linuxkit qemu python3
pip3 install pyyaml

# Linux (Debian/Ubuntu)
sudo apt-get install -y qemu-system-x86 python3-yaml
curl -fsSL https://github.com/linuxkit/linuxkit/releases/latest/download/linuxkit-linux-amd64 \
  | sudo tee /usr/local/bin/linuxkit && sudo chmod +x /usr/local/bin/linuxkit
```

---

## Quick start

```bash
# 1. Clone / enter repo
cd bonnie-cicd/

# 2. Point to your bonnie Docker image
export BONNIE_IMAGE=registry.example.com/bonnie:latest
docker pull "$BONNIE_IMAGE"

# 3. Build (produces output/bonnie-cicd.*)
bash build.sh build

# 4. Smoke-test boot in QEMU
bash build.sh smoke

# 5. Push to registry (optional)
export PUSH_REGISTRY=registry.example.com
bash build.sh push
```

### Build modes

| Command | Effect |
|---------|--------|
| `bash build.sh lint` | YAML validation only |
| `bash build.sh build` | Full build + SBOM |
| `bash build.sh smoke` | QEMU boot test |
| `bash build.sh push` | Push to `$PUSH_REGISTRY` |
| `bash build.sh all` | All of the above |

### Output formats

Set `FORMAT=<value>` before building:

| Value | Output | Use case |
|-------|--------|----------|
| `raw` | `.img` | Bare-metal / cloud (default) |
| `iso` | `.iso` | ISO boot, VirtualBox |
| `qcow2` | `.qcow2` | KVM / QEMU |
| `vmdk` | `.vmdk` | VMware |
| `kernel+initrd` | kernel + initrd.img | PXE / iPXE / direct QEMU |

---

## Services

### bonnie (proprietary build system)
Runs as a long-lived daemon, exposing a gRPC endpoint on
`unix:///run/bonnie/bonnie.sock` and Prometheus metrics on `:9091`.

Environment variables forwarded into the container:

| Variable | Default | Purpose |
|----------|---------|---------|
| `BONNIE_CONTAINERD_SOCKET` | `/run/containerd/containerd.sock` | CRI connection |
| `BONNIE_NYDUS_SOCKET` | `/run/containerd-nydus/…grpc.sock` | Nydus snapshotter |
| `BONNIE_STARGZ_SOCKET` | `/run/containerd-stargz-grpc/…sock` | Stargz snapshotter |
| `BONNIE_CACHE_DIR` | `/cache/bonnie` | Layer cache |
| `BONNIE_LOG_LEVEL` | `warn` | Log verbosity |
| `BONNIE_METRICS_ADDR` | `0.0.0.0:9091` | Prometheus |

### nydus-snapshotter
Enables **lazy image pulling** via FUSE-based chunk streaming.
- Daemon mode: `shared` (one nydusd per node, not per container)
- Blob cache: 20 GiB on-disk at `/var/lib/nydus/cache`
- Prefetch workers: 4

### stargz-snapshotter
Complementary lazy-pull for **eStargz** and **SOCI**-formatted images.
- Background prefetch: 10 MiB chunks, 500 ms poll
- Configured with GCR mirror for docker.io

### QEMU / binfmt
- `tonistiigi/binfmt` registers all architectures (arm64, riscv64, s390x …)
- `multiarch/qemu-user-static` provides the emulation binaries
- Enables multi-arch image builds inside bonnie without a remote builder

### buildkitd
- Rootless-capable; runs in the `buildkit` containerd namespace
- Uses overlayfs snapshotter; GC limit 10 GiB

---

## Security hardening

### Kernel
| Feature | Status |
|---------|--------|
| Page-table isolation (PTI/KPTI) | ✅ Enabled |
| Retpoline + IBPB | ✅ Enabled |
| KASLR + ASLR (randomize_memory) | ✅ Enabled |
| INIT_ON_ALLOC / INIT_ON_FREE | ✅ Enabled |
| Hardened usercopy | ✅ Enabled |
| Module signature enforcement | ✅ SHA-512 |
| BPF JIT hardening (level 2) | ✅ Enabled |
| Unprivileged BPF disabled | ✅ Enabled |
| vsyscall disabled | ✅ `vsyscall=none` |
| debugfs disabled | ✅ Cmdline |
| SLAB freelist randomised | ✅ Enabled |

### Userspace
- **AppArmor** default mandatory access for all services
- **seccomp** filters applied via runc OCI runtime
- **cgroups v2** only — no v1 subsystems
- **Read-only rootfs** — writable state only on tmpfs mounts
- **Principle of least privilege** — each service declares only the CAPs it needs
- **No SSH** in default build (getty disabled); enable explicitly for debug
- **SBOM** generated at every build and attested via GitHub Actions

### sysctl highlights
- `kernel.kptr_restrict=2` — kernel pointers never leaked to userspace
- `kernel.dmesg_restrict=1` — dmesg requires CAP_SYSLOG
- `kernel.yama.ptrace_scope=2` — only admin can ptrace
- `net.core.bpf_jit_harden=2` — constant blinding in JIT
- `net.ipv4.tcp_syncookies=1` — SYN flood protection

---

## Performance tuning

### Boot time targets

| Milestone | Target |
|-----------|--------|
| Kernel start → init | < 0.5 s |
| containerd ready | < 2 s |
| nydus-snapshotter ready | < 3 s |
| bonnie accepting RPCs | < 4 s |
| **Full system ready** | **< 5 s** |

### Network (TCP BBR + FQ)
BBR congestion control with Fair Queue discipline eliminates head-of-line
blocking in high-throughput registry pulls during parallel CI jobs.

### Lazy image pulling
With nydus or stargz, CI jobs start executing **before** the full image is
downloaded — only the requested file chunks are fetched on demand:

```
Traditional pull:  [====download====][run]        ~25 s for 2 GB image
Nydus lazy pull:   [d][run+fetch in parallel]     ~4 s to first process
```

### io_uring
containerd and buildkitd can leverage io_uring for high-throughput async I/O
on layer extraction and snapshot operations.

---

## Metrics

| Exporter | Address | Key metrics |
|----------|---------|-------------|
| node-exporter | `:9100/metrics` | CPU, mem, disk, net |
| bonnie | `:9091/metrics` | Build queue, duration, cache hit-rate |
| containerd | `:1338/metrics` | Container starts, pull latency |

Scrape with Prometheus and alert on:
- `bonnie_build_queue_depth > 10` (backpressure)
- `bonnie_cache_hit_ratio < 0.5` (cold cache)
- `container_pull_latency_seconds{quantile="0.99"} > 30`

---

## Customisation

### Replace bonnie image
Edit `bonnie.yml`, services → bonnie → `image`:
```yaml
- name: bonnie
  image: your-registry.example.com/bonnie:v1.2.3
```

### Add a private registry mirror
Add to `files` in `bonnie.yml`:
```yaml
- path: /etc/containerd/certs.d/your-registry.example.com/hosts.toml
  contents: |
    server = "https://your-registry.example.com"
    [host."https://your-mirror.example.com"]
      capabilities = ["pull", "resolve"]
```

### Enable debug console
Change `INSECURE=false` → `INSECURE=true` in the getty service
and add your public SSH key to `/etc/linuxkit/authorized_keys`.

### Custom kernel config
Edit `kernel-config-fragments/bonnie-cicd.config`, then build a custom kernel:
```bash
linuxkit pkg build --org your-org kernel-config-fragments/
# update bonnie.yml kernel.image to the new digest
```

---

## Directory layout

```
bonnie-cicd/
├── bonnie.yml                          # Main LinuxKit definition (start here)
├── build.sh                            # Build / test / push helper
├── kernel-config-fragments/
│   └── bonnie-cicd.config              # Kernel Kconfig fragment
├── .github/
│   └── workflows/
│       └── bonnie-linuxkit.yml         # GitHub Actions CI pipeline
└── output/                             # Build artefacts (git-ignored)
    ├── bonnie-cicd.img
    ├── bonnie-cicd-kernel
    ├── bonnie-cicd-initrd.img
    └── sbom-<tag>.json
```

---

## Upgrading components

All images in `bonnie.yml` are pinned to explicit tags.  
Run this helper to find new releases:

```bash
# Check linuxkit base images
linuxkit pkg show-tag linuxkit/kernel linuxkit/containerd linuxkit/init

# Check third-party images
docker run --rm ghcr.io/containerd/nydus-snapshotter --version
docker run --rm ghcr.io/containerd/stargz-snapshotter --version
```

Update the `image:` fields and re-run `bash build.sh all`.

---

## License

Internal tooling — proprietary.  
Third-party components are used under their respective open-source licences
(LinuxKit: Apache 2.0, containerd: Apache 2.0, nydus: Apache 2.0,
stargz-snapshotter: Apache 2.0, QEMU: GPL-2.0).
