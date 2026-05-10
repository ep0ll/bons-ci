# bonnie-cicd — LinuxKit Image

Minimal · Immutable · Sub-5-second Boot · Hardened Kernel · CI/CD Native

A production-grade LinuxKit image for the bonnie proprietary build system,
with first-class lazy image pulling (nydus, stargz), multi-arch emulation
(QEMU), and a fully locked-down security posture.

---

## Architecture

    +--------------------------------------------------------------------------+
    |                      bonnie-cicd LinuxKit Image                         |
    |  Kernel 6.6 LTS  cgroups-v2-only  AppArmor+Landlock+Lockdown(integ.)  |
    |  PREEMPT_NONE  HZ=100  BBR+FQ  io_uring  BPF-JIT-always-on            |
    |                                                                          |
    |  ONBOOT (sequential, run-to-completion):                                 |
    |  rngd -> cgroupfs-mount -> sysctl -> dhcpcd -> mount-state ->           |
    |  format-state -> metadata -> qemu-binfmt -> hugepages -> modules        |
    |                                                                          |
    |  +------------+  +--------------+  +---------------+  +-------------+  |
    |  | containerd |  |    nydus-    |  |   stargz-     |  |  buildkitd  |  |
    |  | (CRI+OCI)  |  | snapshotter  |  | snapshotter   |  |  10 GiB GC  |  |
    |  | :1338 prom |  | shared-daemon|  | eStargz/SOCI  |  | unix socket |  |
    |  | overlayfs  |  | 20 GiB cache |  |  :9092 prom   |  |             |  |
    |  +-----+------+  +------+-------+  +------+--------+  +------+------+  |
    |        |   proxy_plugin  |   proxy_plugin  |                  |         |
    |        +--------+--------+                 |                  |         |
    |                 |                          |                  |         |
    |  +--------------+-----------------------------------------------------------+
    |  |              bonnie (proprietary build system)                           |
    |  |  unix:///run/bonnie/bonnie.sock  :9091 Prometheus                       |
    |  |  BONNIE_SNAPSHOTTER_PRIORITY=nydus,stargz,overlayfs                     |
    |  +--------------------------------------------------------------------------+
    |                                                                          |
    |  +-------------------+  +--------------------+  +--------------------+  |
    |  |   qemu-binfmt     |  |   node-exporter    |  |       getty        |  |
    |  | arm64 riscv64     |  |   :9100 Prometheus |  |  INSECURE=false    |  |
    |  | s390x ppc64 mips  |  | cpu mem disk net   |  |  (debug only)      |  |
    |  +-------------------+  +--------------------+  +--------------------+  |
    +--------------------------------------------------------------------------+

---

## Boot Timeline (targets)

      0 ms   kernel start (zstd-decompressed image)
    200 ms   initrd mounted, /init running
    400 ms   rngd seeded — entropy available
    600 ms   cgroups v2 mounted, all sysctl applied
    700 ms   DHCP lease acquired
   ----      -------------------------------------------
   2000 ms   containerd unix socket ready
   2500 ms   nydus-snapshotter ready
   3000 ms   stargz-snapshotter ready
   3500 ms   buildkitd ready
   4000 ms   bonnie accepting RPCs
   4500 ms   node-exporter scrape-ready
   ----      -------------------------------------------
  <5000 ms   FULL SYSTEM READY

Run: make benchmark

---

## Lazy Image Pulling

    Traditional pull:
      Job queued  [========= download 2 GB =========]  container starts
                                 ~25 s (cold)

    Nydus (warm blob cache):
      Job queued  [d]  container starts, chunks streamed on-demand
                  ~1 s

    Nydus (cold cache):
      Job queued  [==]  container starts, background prefetch continues
                  ~4 s  (only metadata + first-accessed chunks)

bonnie selects the snapshotter automatically:
  BONNIE_SNAPSHOTTER_PRIORITY=nydus,stargz,overlayfs

---

## Security Layers

  Kernel:     KASLR, PTI/KPTI, Retpoline, CFI, Shadow Call Stack
  Kernel:     INIT_ON_ALLOC + INIT_ON_FREE (zero all freed memory always)
  Kernel:     SLAB freelist randomisation and hardening
  Kernel:     Module signing SHA-512 forced, no unsigned modules
  Kernel:     Lockdown=integrity (no live patching from userspace)
  Kernel:     Landlock + Yama + BPF LSM enabled via lsm= cmdline
  Kernel:     vsyscall=none, debugfs=off, slab_nomerge, page_poison=1
  Kernel:     init_on_alloc=1, init_on_free=1 on cmdline too
  Userspace:  AppArmor enforce profiles on bonnie and containerd
  Userspace:  seccomp syscall allowlist per service (runtime/seccomp-bonnie.json)
  Userspace:  OCI spec noNewPrivileges + masked /proc paths (runtime/oci-spec-patch.json)
  Userspace:  cgroups v2 only, no v1 hierarchy (cgroup_no_v1=all)
  Userspace:  Read-only rootfs everywhere, all writable state on tmpfs
  Userspace:  Least-privilege capabilities declared per service
  Integrity:  IMA measurement + appraisal + audit (PCR 10) via runtime/ima-policy
  Network:    SYN cookies, RP filter, no redirects, no source routing
  Network:    BPF JIT harden=2, unprivileged BPF disabled
  Network:    BBR + FQ for registry throughput (no head-of-line blocking)
  Supply:     All init/service images pinned by sha256 digest
  Supply:     SBOM generated and attested at every build
  Supply:     cosign keyless signature via Sigstore on every publish

---

## Prerequisites

  linuxkit         v1.2.0+   github.com/linuxkit/linuxkit/releases
  docker           24.x      docs.docker.com/get-docker
  make             4.x       apt install make  OR  brew install make
  python3+pyyaml   3.11      pip3 install pyyaml
  qemu (optional)  8.x       smoke tests and benchmark only
  cosign (opt.)    2.x       publish pipeline signing only
  crane (opt.)     0.19      faster digest pinning in make pin

Install linuxkit (Linux amd64):
  curl -fsSL \
    https://github.com/linuxkit/linuxkit/releases/download/v1.2.0/linuxkit-linux-amd64 \
    | sudo tee /usr/local/bin/linuxkit && sudo chmod +x /usr/local/bin/linuxkit

Install linuxkit (macOS):
  brew install linuxkit

Python dep:
  pip3 install pyyaml

---

## Quick Start

  export BONNIE_IMAGE=registry.example.com/bonnie:v1.2.3
  make lint          # validate YAML
  make build         # output/bonnie-cicd.img
  make build-krd     # output/bonnie-cicd-kernel + initrd.img
  make smoke         # QEMU 20-second boot test
  make benchmark     # boot waterfall with ms timing
  PUSH_REGISTRY=ghcr.io/my-org make push sign

---

## All Make Targets

  make build        Build raw image (default)
  make build-iso    Build bootable ISO
  make build-krd    Build kernel + initrd (required for smoke/benchmark)
  make build-qcow2  Build QCOW2 for KVM/QEMU
  make build-vmdk   Build VMDK for VMware
  make smoke        QEMU boot smoke test (20 s, panic check)
  make benchmark    Full boot waterfall with ms timing and pass/warn/fail
  make verify       In-VM security posture checker (run inside the VM)
  make pin          Resolve all image:tag to image:tag@sha256:...
  make sbom         Generate Software Bill of Materials JSON
  make push         Push to PUSH_REGISTRY
  make sign         cosign keyless signature (Sigstore)
  make lint         YAML validation only
  make clean        Remove output/
  make info         Print current configuration
  make all          Full pipeline: lint, build, smoke, benchmark, push, sign

---

## File Layout

  bonnie-cicd/
  |-- bonnie.yml                        Main LinuxKit definition (start here)
  |-- Makefile                          Ergonomic build interface
  |-- build.sh                          Core build/smoke/push script
  |-- README.md                         This file
  |-- CHANGELOG.md                      Release history
  |
  |-- kernel-config-fragments/
  |   +-- bonnie-cicd.config            Kconfig fragment (200+ options)
  |
  |-- runtime/
  |   |-- seccomp-bonnie.json           Strict seccomp syscall allowlist
  |   |-- oci-spec-patch.json           OCI runtime baseline
  |   +-- ima-policy                    IMA measurement + appraisal policy
  |
  |-- scripts/
  |   |-- pin-digests.sh               Resolve image tags to sha256 digests
  |   |-- benchmark-boot.sh            Boot waterfall benchmark (QEMU)
  |   +-- verify-security.sh           In-VM security posture checker
  |
  |-- .github/
  |   +-- workflows/
  |       +-- bonnie-linuxkit.yml       CI: lint, build, smoke, scan, publish
  |
  +-- output/                           Build artefacts (git-ignored)
      |-- bonnie-cicd.img
      |-- bonnie-cicd-kernel
      |-- bonnie-cicd-initrd.img
      |-- sbom-<tag>.json
      |-- boot-report-<ts>.json
      +-- smoke-<tag>.log

---

## bonnie Service Variables

  BONNIE_CONTAINERD_SOCKET    /run/containerd/containerd.sock
  BONNIE_NYDUS_SOCKET         /run/containerd-nydus/containerd-nydus-grpc.sock
  BONNIE_STARGZ_SOCKET        /run/containerd-stargz-grpc/containerd-stargz-grpc.sock
  BONNIE_BUILDKIT_SOCKET      /run/buildkit/buildkitd.sock
  BONNIE_SNAPSHOTTER_PRIORITY nydus,stargz,overlayfs
  BONNIE_MAX_PARALLEL_JOBS    32
  BONNIE_CACHE_DIR            /cache/bonnie (tmpfs, 10 GiB)
  BONNIE_METRICS_ADDR         0.0.0.0:9091
  BONNIE_GRPC_ADDR            unix:///run/bonnie/bonnie.sock
  GOMAXPROCS                  0 (auto from cgroup quota)
  GOMEMLIMIT                  4GiB

---

## Prometheus Metrics

  containerd          :1338   pull latency, container starts, snapshot ops
  nydus-snapshotter   :9090   cache hit ratio, chunk fetch duration
  bonnie              :9091   build queue depth, job duration, cache hit rate
  stargz-snapshotter  :9092   prefetch queue depth, fetch errors
  node-exporter       :9100   CPU, mem, disk, net, hugepages, PSI pressure

Recommended alert rules (Prometheus):

  bonnie_boot_seconds > 5                         => BootTooSlow
  bonnie_build_queue_depth > 10 for 2m            => BuildQueueBackpressure
  bonnie_cache_hit_ratio < 0.5 for 5m             => LowCacheHitRate
  nydus p99 chunk fetch > 2 s for 5m              => NydusChunkFetchSlow

---

## Customisation

Replace bonnie image:
  Edit bonnie.yml -> services -> bonnie -> image:
    image: your-registry.example.com/bonnie:v2.0.0@sha256:<digest>

Add a registry mirror:
  Add a file entry in bonnie.yml -> files:
    path: /etc/containerd/certs.d/your-registry.example.com/hosts.toml
    contents: |
      server = "https://your-registry.example.com"
      [host."https://your-mirror.internal"]
        capabilities = ["pull", "resolve"]

Enable debug console (emergency only):
  Set INSECURE=true in the getty service env and rebuild.

Adjust CPU isolation (set upper bound to nproc-1):
  Edit bonnie.yml kernel.cmdline:
    isolcpus=2-63 nohz_full=2-63 rcu_nocbs=2-63 irqaffinity=0-1

Increase nydus blob cache:
  Edit nydus-snapshotter config.toml in bonnie.yml:
    cache_size = "40Gi"
  Also increase the tmpfs mount:
    options: ["size=45g"]

Custom kernel build:
  Edit kernel-config-fragments/bonnie-cicd.config
  Then: scripts/kconfig/merge_config.sh arch/x86/configs/x86_64_defconfig \
          bonnie-cicd/kernel-config-fragments/bonnie-cicd.config
  Build with linuxkit pkg build and update bonnie.yml kernel.image digest.

---

## Upgrading

  1. Update image tags in bonnie.yml
  2. make pin             (resolve to sha256)
  3. git diff bonnie.yml  (review)
  4. make all             (full pipeline)

---

## Contributing

  1. Branch from main
  2. Edit bonnie.yml / scripts
  3. make lint must pass
  4. make smoke must pass
  5. make benchmark — total boot must be under 5000 ms with KVM
  6. make verify — all security checks must be PASS
  7. Open PR; CI enforces every step above

---

## Licence

Internal tooling, proprietary.
Third-party components retain their upstream licences:
LinuxKit (Apache-2.0), containerd (Apache-2.0), nydus (Apache-2.0),
stargz-snapshotter (Apache-2.0), QEMU (GPL-2.0), Linux kernel (GPL-2.0).
