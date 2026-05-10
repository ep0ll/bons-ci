# bonnie-cicd Operations Runbook

Procedures for operating bonnie-cicd in production CI environments.
All commands assume you are inside the VM (via getty or SSH) unless noted.

---

## Table of Contents

1. Daily Operations
2. Image Upgrades
3. Service Recovery Procedures
4. nydus Blob Cache Management
5. Kernel Upgrades
6. Scaling
7. Disaster Recovery
8. On-Call Escalation Matrix

---

## 1. Daily Operations

### Health check
```bash
# Quick status — exits 0=ok, 1=degraded, 2=critical
bash /etc/bonnie/health-check.sh

# JSON output for automation
bash /etc/bonnie/health-check.sh --json | jq .

# Silent mode for cron (only prints on failure)
bash /etc/bonnie/health-check.sh --quiet
```

### Check build queue
```bash
# Via Prometheus metric
curl -s http://localhost:9091/metrics \
  | grep bonnie_build_queue_depth

# Via bonnie gRPC (if bonnie ships a CLI)
bonniectl queue status
```

### Check service sockets
```bash
for sock in \
  /run/containerd/containerd.sock \
  /run/containerd-nydus/containerd-nydus-grpc.sock \
  /run/containerd-stargz-grpc/containerd-stargz-grpc.sock \
  /run/buildkit/buildkitd.sock \
  /run/bonnie/bonnie.sock; do
  [ -S "$sock" ] && echo "OK   $sock" || echo "MISS $sock"
done
```

### View live logs (structured JSON)
```bash
# bonnie logs
ctr -n bonnie task exec --exec-id logs bonnie \
  cat /proc/1/fd/1 2>/dev/null | jq .

# containerd logs
journalctl -u containerd -f 2>/dev/null \
  || ctr events 2>/dev/null

# nydus logs
ctr -n nydus task exec --exec-id logs nydus-snapshotter \
  tail -f /var/log/nydus-snapshotter.log
```

### Check nydus cache hit rate
```bash
curl -s http://localhost:9090/metrics \
  | grep -E "nydus_cache_(hit|miss)"
# Hit rate = hits / (hits + misses)
```

### List running containers
```bash
ctr --namespace default containers list
ctr --namespace buildkit containers list
```

---

## 2. Image Upgrades

### Zero-downtime image upgrade procedure

bonnie-cicd is immutable. To upgrade, you build a new image and replace
the running node. The procedure is:

**Step 1: Build new image**
```bash
# On your dev machine
cd bonnie-cicd/
export BONNIE_IMAGE=your-registry/bonnie:v2.0.0

# Update component versions in bonnie.yml, then:
make pin       # resolve to digests
make lint      # validate
make build-krd # build kernel+initrd
make smoke     # boot test
make benchmark # timing regression check
make push      # push to registry
make sign      # cosign sign
```

**Step 2: Pre-warm nydus cache on the target node**
```bash
# Pull all images that bonnie jobs need into nydus format
# before the new node starts serving traffic
nydus-image convert \
  --source docker://your-registry/build-tool:latest \
  --target nydus-registry/build-tool:latest-nydus
```

**Step 3: Drain the node**
```bash
# Tell bonnie to stop accepting new jobs but finish current ones
bonniectl node drain --graceful --timeout=300s

# Wait for queue to empty
watch -n5 'curl -s http://localhost:9091/metrics | grep bonnie_build_queue_depth'
```

**Step 4: Replace node**
```bash
# Cloud: replace the instance with the new image AMI/disk
# Bare metal: use iPXE or netboot to boot the new kernel+initrd

# The new image boots fresh from the new bonnie.yml definition
# nydus blob cache is on tmpfs — a new pre-warm run may be needed
```

**Step 5: Verify**
```bash
bash scripts/verify-security.sh   # inside the new node
bash scripts/benchmark-boot.sh    # boot timing regression
bash scripts/health-check.sh      # all services healthy
```

### Emergency rollback
```bash
# If the new image is broken, boot the previous kernel+initrd
# Store last-known-good image in your CI registry with a stable tag:
# your-registry/bonnie-cicd:stable  (points to last verified release)

# Netboot / kexec the previous image:
kexec -l output/bonnie-cicd-kernel-PREVIOUS \
      --initrd=output/bonnie-cicd-initrd.img-PREVIOUS \
      --append="console=ttyS0 quiet panic=1"
kexec -e
```

---

## 3. Service Recovery Procedures

### containerd is unresponsive

**Symptoms:** `ctr containers list` hangs; bonnie reports "containerd unavailable".

**Diagnosis:**
```bash
# Check if the socket exists
ls -la /run/containerd/containerd.sock

# Check if the process is alive
ps aux | grep containerd

# Check for lock contention
lsof /run/containerd/containerd.sock

# Check disk space on the containerd tmpfs
df -h /var/lib/containerd
```

**Recovery:**
```bash
# 1. Stop all child processes first to avoid orphaned runc shims
for pid in $(ps aux | grep containerd-shim | awk '{print $2}'); do
  kill -TERM "$pid" 2>/dev/null || true
done

# 2. The LinuxKit service supervisor will restart containerd automatically
# If not, trigger a restart via the service socket:
ctr services restart containerd 2>/dev/null || true

# 3. If containerd state is corrupted, clear it (DESTRUCTIVE — all containers gone):
# rm -rf /var/lib/containerd/io.containerd.content.v1.content/tmp/
# The state will rebuild from the registry on next pull
```

### nydus-snapshotter is unresponsive

**Symptoms:** Image pulls fall back to overlayfs; job cold-start times revert to ~25 s.

**Diagnosis:**
```bash
curl -s http://localhost:9090/metrics | head -5
ls -la /run/containerd-nydus/
dmesg | grep -i fuse | tail -20
```

**Recovery:**
```bash
# 1. Unmount all FUSE mounts gracefully
umount -l /var/lib/containerd/snapshotter/nydus/snapshots/*/fs 2>/dev/null || true

# 2. The supervisor will restart nydus-snapshotter
# Check blob cache integrity:
ls -lh /var/lib/nydus/cache/ | tail -5

# 3. If the FUSE device is stuck:
[ -c /dev/fuse ] || mknod /dev/fuse c 10 229

# 4. Run cache GC if cache is near-full (may have caused the failure):
bash /etc/bonnie/nydus-cache-gc.sh
```

### bonnie is unresponsive

**Symptoms:** CI jobs fail immediately with "connection refused"; gRPC socket missing.

**Diagnosis:**
```bash
ls -la /run/bonnie/bonnie.sock
curl -s http://localhost:9091/metrics
ps aux | grep bonnie

# Check if bonnie is deadlocked (Go goroutine dump):
kill -SIGABRT $(pgrep bonnie) 2>/dev/null  # triggers goroutine dump to stderr
```

**Recovery:**
```bash
# 1. The LinuxKit supervisor will restart bonnie automatically
# Force a restart by removing the stale socket:
rm -f /run/bonnie/bonnie.sock

# 2. If bonnie is OOM-killed repeatedly, reduce parallel jobs:
# Set BONNIE_MAX_PARALLEL_JOBS lower in bonnie.yml and rebuild

# 3. Clear build cache if it is corrupted:
# rm -rf /cache/bonnie/*   (DESTRUCTIVE — cache will rebuild from registry)
```

### buildkitd is unresponsive

**Symptoms:** `docker build` via bonnie fails; buildkitd socket missing.

**Diagnosis:**
```bash
ls -la /run/buildkit/buildkitd.sock
curl -s http://localhost:1338/metrics  # containerd, not buildkit
df -h /var/lib/buildkit
```

**Recovery:**
```bash
# 1. Clear stale build sessions:
rm -f /run/buildkit/buildkitd.sock

# 2. If disk is full, run GC:
buildctl prune --all 2>/dev/null || true
rm -rf /var/lib/buildkit/snapshots/overlayfs/snapshots/

# 3. The supervisor restarts buildkitd automatically
```

---

## 4. nydus Blob Cache Management

### Check cache size
```bash
du -sh /var/lib/nydus/cache/
df -h /var/lib/nydus/
```

### Manual GC
```bash
# Dry run (shows what would be deleted)
bash /etc/bonnie/nydus-cache-gc.sh --dry-run

# Live GC (evicts LRU blobs until cache <= 14 GiB)
bash /etc/bonnie/nydus-cache-gc.sh

# Check GC log
tail -20 /var/log/nydus-cache-gc.log | jq .
```

### Pre-warm cache for a new image
```bash
# Convert a Docker image to nydus format and pre-pull to cache
nydus-image convert \
  --source  docker://your-registry/heavy-build-tool:latest \
  --target  nydus-registry/heavy-build-tool:latest-nydus \
  --work-dir /var/lib/nydus/cache

# The next job that needs this image will get it from the local cache
```

### Inspect cache entries
```bash
# List cached blob files with sizes and access times
find /var/lib/nydus/cache -name '*.blob' \
  -printf '%A@ %s %p\n' \
  | sort -n \
  | awk '{printf "atime=%s size_mib=%d %s\n", strftime("%Y-%m-%d %H:%M",int($1)), $2/1048576, $3}'
```

---

## 5. Kernel Upgrades

Kernel upgrades require a full image rebuild. There is no in-place kernel
upgrade on an immutable LinuxKit image.

### Upgrade procedure
```bash
# 1. Update kernel version in bonnie.yml
#    Change: image: linuxkit/kernel:6.6.22
#    To:     image: linuxkit/kernel:6.6.<new>

# 2. Re-pin the digest
make pin

# 3. If using custom kernel, rebuild it:
export KERNEL_VERSION=6.6.<new>
export KERNEL_REGISTRY=your-registry
bash scripts/build-kernel.sh

# 4. Run full validation
make build-krd
make smoke
make benchmark
make verify    # security regression check

# 5. Check for config regressions
#    The kernel team may have changed Kconfig dependencies:
#    Compare new .config with kernel-config-fragments/bonnie-cicd.config
```

### Test a new kernel without rebuilding the full image
```bash
# QEMU direct boot with the candidate kernel
qemu-system-x86_64 \
  -nographic -no-reboot -m 1024M -smp 2 \
  -enable-kvm \
  -kernel /path/to/new/bzImage \
  -initrd output/bonnie-cicd-initrd.img \
  -append "console=ttyS0 quiet loglevel=0 panic=1 systemd.unified_cgroup_hierarchy=1 cgroup_no_v1=all"
```

---

## 6. Scaling

### Horizontal scaling (add nodes)

bonnie-cicd nodes are stateless. Add a node by:
1. Booting a new VM/bare metal with the same `bonnie-cicd` image.
2. Connecting it to your load balancer or build queue.
3. The nydus blob cache starts cold; pre-warm it (see §4).

**Recommended minimum node spec:**
- vCPUs: 8+ (2 for OS, 6+ for bonnie workers)
- RAM: 16 GiB (8 GiB for nydus cache tmpfs + 8 GiB for builds)
- Network: 10 Gbps (registry pull bottleneck)
- Disk: none required (all tmpfs)

### Vertical scaling (more CPUs on same node)

Update `isolcpus` range in `bonnie.yml` cmdline and `BONNIE_MAX_PARALLEL_JOBS`:
```yaml
# bonnie.yml
cmdline: >-
  isolcpus=2-63
  nohz_full=2-63
  rcu_nocbs=2-63
```
```yaml
# bonnie service env
- BONNIE_MAX_PARALLEL_JOBS=60
```

Rebuild and redeploy.

### Tune nydus cache for larger nodes
```yaml
# bonnie.yml — nydus-snapshotter runtime.mounts
- type: tmpfs
  dest: /var/lib/nydus
  options: ["size=50g"]   # was 25g
```
```toml
# nydus config.toml
[device.cache]
  cache_size = "40Gi"    # was 20Gi
```

---

## 7. Disaster Recovery

### Full node loss

Since the rootfs is immutable and all state is on tmpfs, a lost node loses:
- In-flight build jobs (clients will retry)
- nydus blob cache (rebuilds on next pull, ~4 s cold-start overhead)
- bonnie in-memory queue (clients will retry)

Nothing persists to disk. Recovery is simply booting a new node from the
same `bonnie-cicd` image.

### Corrupted nydus blob cache

```bash
# Stop nydus-snapshotter first (via service supervisor)
# Then wipe the cache:
rm -rf /var/lib/nydus/cache/*

# Remove all nydus snapshots in containerd:
ctr snapshots --snapshotter nydus rm $(ctr snapshots --snapshotter nydus list -q) 2>/dev/null || true

# Restart nydus-snapshotter — cache will rebuild on demand
```

### containerd state corruption

```bash
# Wipe containerd state (DESTRUCTIVE — all container state gone)
rm -rf /var/lib/containerd/io.containerd.content.v1.content/tmp/
rm -rf /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/

# containerd reconstructs on restart from registry
```

### bonnie state recovery

bonnie is designed to be stateless across restarts. All build state lives in:
- The client (which queued the job)
- The OCI registry (which holds the image layers)

On restart, bonnie reconnects to all sockets and accepts new jobs.
No manual state recovery is needed.

---

## 8. On-Call Escalation Matrix

| Alert | Severity | First Responder | Escalate To |
|-------|----------|-----------------|-------------|
| BootTooSlow | Warning | On-call | Infra team |
| BuildQueueBackpressure | Warning | On-call | Capacity planning |
| BuildQueueCritical | Critical | On-call → Infra | Engineering lead |
| BuildJobsStalled | Critical | On-call | bonnie team |
| LowCacheHitRate | Warning | On-call | Infra team |
| NydusSnapshotterDown | Critical | On-call | Infra team |
| ContainerdDown | Critical | On-call | Infra team |
| OOMKillDetected | Critical | On-call | Infra team |
| TmpfsDiskPressure | Critical | On-call | Infra team |
| AppArmorViolation | Warning | Security team | Security lead |
| HighCPUSteal | Warning | On-call | Cloud/host team |
| NydusChunkFetchSlow | Warning | On-call | Infra team |

**Runbook links:**
- `BootTooSlow` → §2 (Image Upgrades), §5 (Kernel Upgrades)
- `NydusSnapshotterDown` → §3.2 (nydus recovery)
- `ContainerdDown` → §3.1 (containerd recovery)
- `OOMKillDetected` → §3.3 (bonnie recovery), §6 (scaling)
- `TmpfsDiskPressure` → §4 (nydus cache GC)
- `AppArmorViolation` → `docs/security-guide.md` §10
