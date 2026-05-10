# bonnie-cicd Performance Tuning Guide

This guide explains every performance knob in the bonnie-cicd image, why it
exists, and how to tune it for your specific hardware and workload.

---

## Table of Contents

1. Boot Time
2. Kernel Scheduler
3. CPU Isolation and NUMA
4. Memory
5. Network (BBR + FQ)
6. Block I/O
7. Lazy Image Pulling
8. Huge Pages and QEMU
9. Cgroups and Resource Limits
10. Tuning Checklist

---

## 1. Boot Time

### Target: < 5 seconds from kernel start to bonnie accepting RPCs

The image is tuned for fast boot at every layer:

**Kernel compression: zstd**
zstd decompresses 3-4x faster than gzip at similar ratios. Set in
`kernel-config-fragments/bonnie-cicd.config`:
```
CONFIG_RD_ZSTD=y
CONFIG_KERNEL_ZSTD=y
```

**Cmdline: skip hardware enumeration**
```
pci=nommconf    # skip MMCONFIG re-enumeration (saves ~200 ms on some BIOSes)
nousb           # no USB legacy handoff
acpi=noirq      # skip ACPI device enumeration
clocksource=tsc # use TSC immediately; skip HPET probe
tsc=reliable    # skip TSC stability calibration
no_timer_check  # skip NTP-based IRQ calibration
```

**onboot service ordering**
rngd runs first. Everything that needs entropy (DHCP, TLS handshakes,
containerd's key generation) blocks until the CSPRNG is seeded.
Putting rngd last would add 1-3 seconds of entropy wait.

**Service parallelism**
LinuxKit runs all `services` in parallel (unlike `onboot` which is sequential).
All six daemons start simultaneously after onboot completes. The critical path
is: containerd (2 s) -> nydus (2.5 s) -> bonnie (4 s).

**To reduce boot time further:**
- Use a RAM-backed block device so initrd decompression is faster.
- Pre-pull all bonnie tool images into the nydus blob cache before first boot
  using `nydus-image convert` offline.
- On bare metal, enable `CONFIG_PREEMPT_NONE` and `HZ_100` (already done).

---

## 2. Kernel Scheduler

### PREEMPT_NONE + HZ=100

CI/CD workloads are batch jobs, not interactive. `PREEMPT_NONE` eliminates
scheduler interrupts during tight compute loops (compilation, layer extraction).
`HZ=100` means the timer fires 100 times per second instead of 1000, reducing
interrupt overhead by 10x for busy CPUs.

**Do not change this for interactive workloads** (latency spikes will be visible).

### CFS tuning (sysctl)
```
kernel.sched_min_granularity_ns     = 10000000   # 10 ms minimum timeslice
kernel.sched_wakeup_granularity_ns  = 15000000   # 15 ms wakeup preemption
kernel.sched_migration_cost_ns      = 5000000    # 5 ms cache-hot threshold
kernel.sched_nr_migrate             = 256         # tasks moved per balance
kernel.sched_autogroup_enabled      = 0           # disable session grouping
```

`sched_autogroup_enabled=0` is important: autogroup creates implicit cgroups
per terminal session, which conflicts with the explicit cgroup hierarchy that
bonnie uses to manage build jobs.

---

## 3. CPU Isolation and NUMA

### isolcpus + nohz_full + rcu_nocbs

The kernel cmdline in `bonnie.yml` isolates CPUs 2-31 for bonnie workers:
```
isolcpus=2-31
nohz_full=2-31
rcu_nocbs=2-31
rcu_nocb_poll
irqaffinity=0-1
```

This means:
- CPUs 0-1 handle OS threads, IRQs, and kernel housekeeping.
- CPUs 2-31 are fully dedicated to bonnie build jobs.
- The tick is suppressed on isolated CPUs when they have exactly one runnable
  task (nohz_full), eliminating ~1 ms of latency per tick.
- RCU callbacks are offloaded from isolated CPUs to CPU 0-1.

**Tune the upper bound to your hardware:**
```yaml
# bonnie.yml kernel.cmdline
isolcpus=2-<nproc-1>
nohz_full=2-<nproc-1>
rcu_nocbs=2-<nproc-1>
```

On a 4-vCPU VM: `isolcpus=2-3`
On a 64-core bare metal: `isolcpus=2-63`

### NUMA

`numa_balancing=disable` prevents the kernel from migrating bonnie job
processes between NUMA nodes. If bonnie already pins workers to a socket,
NUMA balancing would only add overhead.

On single-socket systems this has no effect.

---

## 4. Memory

### No swap
```
noswap        # kernel cmdline
vm.swappiness = 0   # sysctl belt-and-suspenders
```
CI builds must be deterministic. Swapping introduces non-deterministic latency
spikes. If a build OOMs, it should fail fast with a clear error, not page-thrash
for minutes.

### Overcommit
```
vm.overcommit_memory  = 1   # always allow allocations
vm.overcommit_ratio   = 200
```
Go runtimes (bonnie, containerd) map large virtual regions speculatively.
Without overcommit, many Go programs fail to start on systems with reasonable
physical RAM.

### Dirty page writeback
```
vm.dirty_ratio                 = 20   # start writeback at 20% dirty
vm.dirty_background_ratio      = 5    # background writeback at 5%
vm.dirty_expire_centisecs      = 500  # flush pages older than 5 s
vm.dirty_writeback_centisecs   = 100  # writeback thread wakes every 1 s
```
These settings balance write throughput (layer extraction writes large blobs)
with memory reclaim. More aggressive than defaults.

### Transparent Huge Pages: madvise only
```
transparent_hugepage=madvise
```
QEMU explicitly requests huge pages via `madvise(MADV_HUGEPAGE)`. The kernel
grants them only on request. This avoids THP latency spikes from background
compaction on non-QEMU allocations.

---

## 5. Network: BBR + FQ

### Why BBR?

BBR (Bottleneck Bandwidth and Round-trip propagation time) is Google's
congestion control algorithm. Compared to CUBIC:

- Doesn't fill buffers before detecting congestion (no bufferbloat).
- Maintains near-line-rate throughput even with 1% packet loss.
- Critical for pulling container images over WAN from OCI registries.

```
net.core.default_qdisc               = fq
net.ipv4.tcp_congestion_control       = bbr
```

FQ (Fair Queue) qdisc is the companion to BBR. It paces packets at the
rate BBR prescribes and prevents any single flow from starving others.
Essential for nodes running many parallel registry pulls.

### Socket buffer sizing

For a 10 Gbps link pulling from a registry 50 ms away:
- BDP = 10,000 Mbps * 0.05 s / 8 = 62.5 MB
- Set rmem/wmem max to 512 MB to handle burst:

```
net.core.rmem_max = 536870912   # 512 MiB
net.core.wmem_max = 536870912
net.ipv4.tcp_rmem = 4096 262144 536870912
net.ipv4.tcp_wmem = 4096 131072 536870912
```

### TCP Fast Open
```
net.ipv4.tcp_fastopen = 3   # enable for client and server
```
Eliminates 1 RTT on reconnection to the same registry. On a pull-heavy
workload with many short-lived TCP connections, this is measurable.

---

## 6. Block I/O

### NVMe: no scheduler
```
elevator=none
scsi_mod.use_blk_mq=1
blk_mq.use_io_poll=1
```
Modern NVMe controllers have their own internal queue management. Adding a
software scheduler on top adds latency. `blk_mq.use_io_poll=1` uses CPU
polling instead of interrupts for completion, reducing latency at the cost
of a few % CPU for I/O-heavy operations.

### io_uring
```
CONFIG_IO_URING=y
```
containerd and buildkitd use io_uring for layer extraction when available.
io_uring submits I/O operations without syscall overhead (after setup),
achieving near-DPDK throughput for sequential reads from blob cache.

### dm-verity (optional integrity)
```
CONFIG_DM_VERITY=y
CONFIG_DM_VERITY_VERIFY_ROOTHASH_SIG=y
```
Enable dm-verity on the root device for cryptographic rootfs integrity.
Adds ~1% I/O overhead on read path. Enable with:
```bash
veritysetup format /dev/sda /dev/sdb  # data, hash devices
```

---

## 7. Lazy Image Pulling

### Nydus (preferred)

nydus-snapshotter in `shared` daemon mode means:
- One `nydusd` process serves ALL containers on the node.
- The blob cache is shared — if container A pulled chunk X, container B gets
  it from cache instantly.
- First access of a file causes a FUSE read that fetches only that chunk.

**Tuning blob cache:**
```toml
# in nydus-snapshotter config.toml
[device.cache]
  cache_size = "20Gi"    # increase if you have more RAM-backed tmpfs
prefetch_workers = 8     # parallel chunk fetch threads
merging_size = "4Mi"     # coalesce reads < 4 MiB into single fetches
```

**Warm-up the cache before peak CI:**
```bash
# Pre-pull nydus-formatted images during off-peak hours
nydus-image convert --source docker://registry/image:tag \
  --target nydus-registry/image:tag-nydus
```

### Stargz (fallback)

stargz-snapshotter handles images that are not nydus-formatted.
```toml
[background_fetch]
  fetch_period_msec = 200     # poll every 200 ms
  prefetch_size     = 16777216  # 16 MiB prefetch window
```
Tune `prefetch_size` up for images with large files (e.g. ML model images).

### Snapshotter priority
```
BONNIE_SNAPSHOTTER_PRIORITY=nydus,stargz,overlayfs
```
bonnie tries each snapshotter in order. If the image is not in nydus format,
it falls through to stargz, then full pull via overlayfs.

---

## 8. Huge Pages and QEMU

### Static huge page pre-allocation

```
hugepages=512   # kernel cmdline: 512 * 2 MiB = 1 GiB pre-allocated
```

QEMU guest memory is backed by huge pages when available. Huge pages:
- Cannot be swapped (good — CI must not page).
- Reduce TLB pressure by 512x compared to 4 KiB pages.
- Are allocated upfront in the onboot phase (before QEMU starts).

**Tune to your QEMU workload:**
- Each QEMU guest using 1 GiB RAM needs 512 huge pages.
- Adjust: `hugepages=<total_qemu_ram_gib * 512>`

### Huge page overcommit
```
vm.nr_overcommit_hugepages=256   # allow 256 extra on-demand allocations
```
Allows QEMU to request slightly more huge pages than were pre-allocated,
up to the overcommit limit, using compaction to satisfy them.

---

## 9. Cgroups v2 Resource Limits

Each service runs in its own cgroup slice. Bonnie manages worker job cgroups
as children of `/system/bonnie`.

**Recommended limits for a 16-vCPU, 32 GiB node:**

```
/system/containerd:   CPUWeight=50,  MemoryMax=4G
/system/nydus:        CPUWeight=20,  MemoryMax=2G
/system/stargz:       CPUWeight=10,  MemoryMax=512M
/system/buildkit:     CPUWeight=80,  MemoryMax=8G
/system/bonnie:       CPUWeight=100, MemoryMax=16G
/system/bonnie/job-*: CPUWeight=100  (per-job slice)
```

Add these as cgroup2 resource files or via `systemd-run` wrappers if running
outside LinuxKit (bare metal systemd).

---

## 10. Tuning Checklist

Before deploying to production, verify each item:

```
[ ] KVM acceleration available on host (check /dev/kvm)
[ ] isolcpus upper bound set to (nproc - 1)
[ ] hugepages=<total_qemu_ram_gib * 512> on cmdline
[ ] BONNIE_MAX_PARALLEL_JOBS matches isolated CPU count
[ ] nydus blob cache tmpfs sized >= cache_size + 20% headroom
[ ] Registry mirror configured in /etc/containerd/certs.d/
[ ] BONNIE_SNAPSHOTTER_PRIORITY=nydus,stargz,overlayfs
[ ] make benchmark passes all milestones under target
[ ] make verify shows all PASS
[ ] Prometheus alerts configured and firing to on-call channel
[ ] Nydus cache GC cron configured (scripts/nydus-cache-gc.sh)
```
