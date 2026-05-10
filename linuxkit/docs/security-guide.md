# bonnie-cicd Security Hardening Guide

This guide documents every security control in bonnie-cicd, its rationale,
and how to verify it is active at runtime.

---

## Table of Contents

1. Kernel Hardening
2. LSM Stack (AppArmor, Landlock, Yama, BPF LSM, Lockdown)
3. seccomp Profiles
4. OCI Runtime Hardening
5. Filesystem Security
6. Network Security
7. Supply Chain Security
8. IMA / Integrity Measurement
9. Capability Model
10. Incident Response

---

## 1. Kernel Hardening

### Memory Safety

| Control | Kernel option / cmdline | What it prevents |
|---------|------------------------|------------------|
| KASLR | `CONFIG_RANDOMIZE_BASE=y` | Stack spray, ROP with known addresses |
| ASLR full | `kernel.randomize_va_space=2` | Heap/stack/mmap spray |
| PTI | `CONFIG_PAGE_TABLE_ISOLATION=y` | Meltdown (CVE-2017-5754) |
| Retpoline | `CONFIG_RETPOLINE=y` | Spectre v2 (CVE-2017-5715) |
| Shadow Call Stack | `CONFIG_SHADOW_CALL_STACK=y` | Control-flow hijacking |
| CFI (Clang) | `CONFIG_CFI_CLANG=y` | Forward-edge CFI |
| Stackleak | `CONFIG_GCC_PLUGIN_STACKLEAK=y` | Stack info leaks, uninit reads |
| INIT_ON_ALLOC | `init_on_alloc=1` (cmdline) | Uninitialised heap reads |
| INIT_ON_FREE | `init_on_free=1` (cmdline) | Use-after-free data leaks |
| Hardened usercopy | `CONFIG_HARDENED_USERCOPY=y` | Kernel-userspace copy overflows |
| SLAB hardening | `CONFIG_SLAB_FREELIST_RANDOM=y` + `HARDENED` | Heap spray patterns |
| Page poison | `page_poison=1` | Freed page reuse |
| Module signing | `CONFIG_MODULE_SIG_FORCE=y` SHA-512 | Malicious kernel module loading |
| No vsyscall | `vsyscall=none` | ROP via fixed vsyscall page |
| No debugfs | `debugfs=off` | Kernel internals exposure |
| No slab merge | `slab_nomerge` | Cross-type slab exploitation |
| Strict devmem | `CONFIG_STRICT_DEVMEM=y` + `IO_STRICT_DEVMEM` | Raw memory access |
| Reset attack | `CONFIG_RESET_ATTACK_MITIGATION=y` | Cold boot key extraction |
| No kexec | `CONFIG_KEXEC=n` | Kernel replacement bypass |

### Spectre / Meltdown

All mitigations are enabled via `mitigations=auto` on the cmdline.
This applies the CPU vendor's recommended mitigation set automatically,
including IBRS/IBPB for Intel and retpoline for all.

**Verify at runtime:**
```bash
grep . /sys/devices/system/cpu/vulnerabilities/*
```
All entries should show "Mitigation: ..." rather than "Vulnerable".

### BPF Hardening

```
kernel.unprivileged_bpf_disabled = 1   # only root/CAP_BPF can load programs
net.core.bpf_jit_harden         = 2   # constant blinding in JIT output
```

`bpf_jit_harden=2` blinds all constants in BPF JIT output, preventing
attackers from using known BPF JIT gadgets in ROP chains.

---

## 2. LSM Stack

The `lsm=` cmdline parameter controls which Linux Security Modules load and
in what order. bonnie-cicd uses:

```
lsm=lockdown,yama,apparmor,bpf,landlock
```

### Lockdown (integrity mode)

Lockdown=integrity prevents:
- Writing to `/dev/mem`, `/dev/kmem`, `/dev/port`
- Loading unsigned kernel modules (belt to module signing's suspenders)
- Hibernation (cold boot attack vector)
- Live kernel patching from userspace
- PCI BAR access from userspace

```bash
# Verify
cat /sys/kernel/security/lockdown
# Expected: [none] integrity [confidentiality]
# (integrity is selected)
```

### Yama

Restricts ptrace to parent processes only (`ptrace_scope=2`):
```
kernel.yama.ptrace_scope = 2   # only admin can ptrace
```
This prevents horizontal ptrace (one unprivileged process spying on another
at the same UID).

### AppArmor

Profiles are in `bonnie.yml` (files section) and `runtime/apparmor.d/`.
Each service has a named profile in enforce mode.

```bash
# Verify
aa-status
# Should show profiles in enforce mode, 0 in complain mode
```

**Adding a profile for a new service:**
```
1. Write the profile in runtime/apparmor.d/<service-name>
2. Add a file entry to bonnie.yml copying it to /etc/apparmor.d/<service-name>
3. The onboot sysctl phase loads AppArmor profiles automatically
```

### Landlock

Landlock provides per-process filesystem access control beyond DAC and
AppArmor. bonnie itself should apply Landlock rules on startup:

```go
// Example: restrict bonnie to only its allowed paths
err := landlock.V3.BestEffort().RestrictPaths(
    landlock.RODirs("/etc/bonnie", "/etc/containerd"),
    landlock.RWDirs("/run/bonnie", "/var/lib/bonnie", "/cache/bonnie"),
)
```

Landlock rules stack with AppArmor — both must permit an action for it to
succeed.

### BPF LSM

The BPF LSM allows dynamic security policy via eBPF programs attached to
LSM hooks. bonnie can attach programs to `bpf_prog_load` to audit or block
BPF program loading by child processes.

---

## 3. seccomp Profiles

The profile is in `runtime/seccomp-bonnie.json`. It uses a deny-by-default
policy (SCMP_ACT_ERRNO) with an explicit allowlist.

**Key denials:**
```
bpf               - no BPF program loading from containers
kexec_*           - no kernel replacement
init_module       - no kernel module loading
finit_module      - no kernel module loading
iopl / ioperm     - no raw port access
ptrace            - no cross-process inspection
perf_event_open   - no hardware performance counters (timing side-channel)
process_vm_writev - no cross-process memory write
setdomainname     - no domain name change
sethostname       - no hostname change
```

**Applying the profile to bonnie-launched containers:**
Reference `runtime/seccomp-bonnie.json` in the containerd runtime options:
```toml
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
  SeccompProfile = "/etc/containerd/seccomp-bonnie.json"
```

**Verify a container's seccomp mode:**
```bash
cat /proc/<pid>/status | grep Seccomp
# 2 = filter mode (seccomp active)
```

---

## 4. OCI Runtime Hardening

`runtime/oci-spec-patch.json` enforces the hardened baseline for every OCI
container bonnie launches:

```json
"noNewPrivileges": true
```
Prevents `setuid` binaries inside containers from gaining privilege via exec.
Even if an attacker drops a setuid binary into a container image, it cannot
elevate.

**Masked paths** hide sensitive kernel files:
```
/proc/acpi, /proc/kcore, /proc/keys, /proc/sched_debug,
/proc/timer_list, /sys/firmware, /sys/kernel/debug
```

**Read-only paths** prevent writes to dangerous kernel interfaces:
```
/proc/bus, /proc/irq, /proc/sys, /proc/sysrq-trigger
```

**Hardened mounts** use `nosuid,noexec,nodev` on all container mounts:
```
/proc  nosuid,noexec,nodev
/dev   nosuid,strictatime
/sys   nosuid,noexec,nodev,ro
/tmp   nosuid,nodev,noexec,mode=1777
```

---

## 5. Filesystem Security

### Read-only rootfs

The LinuxKit image mounts the root filesystem read-only. The `ro` kernel
cmdline parameter and LinuxKit's image format enforce this. The only writable
filesystems are tmpfs mounts explicitly declared per service.

```bash
# Verify
mount | grep ' / '
# Expected: /dev/... on / type ... (ro,...)
```

### State isolation

Each service has its own tmpfs:
- `/run/<service>` for sockets and PIDs
- `/var/lib/<service>` for ephemeral state
- `/cache/bonnie` for the layer cache

These are all in-memory. A system reboot or service restart starts clean.
Persistent state must be explicitly managed via external volumes.

### /tmp: noexec

The `/tmp` mount is `noexec` — shell scripts and binaries dropped into `/tmp`
cannot be executed directly. This blocks a common privilege escalation pattern.

---

## 6. Network Security

### Packet filtering

SYN cookies, RP filter, and ICMP broadcast ignore are all enabled:
```
net.ipv4.tcp_syncookies                = 1   # SYN flood protection
net.ipv4.conf.all.rp_filter            = 1   # no spoofed source IPs
net.ipv4.icmp_echo_ignore_broadcasts   = 1   # no ICMP amplification
net.ipv4.conf.all.accept_redirects     = 0   # no ICMP redirects
net.ipv4.conf.all.send_redirects       = 0
net.ipv4.conf.all.accept_source_route  = 0   # no source routing
net.ipv6.conf.all.accept_ra            = 0   # no rogue IPv6 RA
```

### Listening services

Only these ports should be open:
```
1338  containerd Prometheus metrics (internal)
9090  nydus-snapshotter metrics (internal)
9091  bonnie metrics (internal)
9092  stargz-snapshotter metrics (internal)
9100  node-exporter (internal)
```

No SSH, no admin interface, no unauthenticated ports exposed externally.
Prometheus scrape should be done via an internal network or VPN.

**Verify:**
```bash
ss -tlnp
```

---

## 7. Supply Chain Security

### Image pinning

Every image reference in `bonnie.yml` uses `image: name:tag@sha256:<digest>`.
The digest is verified by Docker/containerd before any layer is extracted.
Tag mutation or registry compromise cannot silently change what runs.

**Keep pins current:**
```bash
make pin        # resolves all :tag to :tag@sha256:...
git diff bonnie.yml
```

### SBOM

Every build generates `output/sbom-<tag>.json` listing all image references
and their digests. This is uploaded as a GitHub Actions artifact and attested
via `actions/attest-build-provenance`.

### cosign (Sigstore)

All published images are signed with cosign in keyless mode (OIDC + Sigstore):
```bash
# Verify a published image
cosign verify \
  --certificate-identity "https://github.com/<org>/bonnie-cicd/.github/workflows/bonnie-linuxkit.yml@refs/heads/main" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ghcr.io/<org>/bonnie-cicd:<tag>
```

### Trivy CVE scanning

The CI pipeline runs Trivy against the SBOM on every build. CRITICAL and HIGH
severity CVEs with fixes available fail the build. Results are uploaded to the
GitHub Security tab as SARIF.

---

## 8. IMA / Integrity Measurement Architecture

The IMA policy in `runtime/ima-policy` measures all executables, shared
libraries, kernel modules, and firmware into PCR 10.

**Viewing measurements:**
```bash
cat /sys/kernel/security/ima/ascii_runtime_measurements | head -20
# Format: PCR algo hash filename
```

**Remote attestation:**
PCR 10 can be quoted by a TPM and verified remotely to prove that only
expected binaries have run. Integrate with:
- HashiCorp Vault's TPM auth method
- Google Cloud's Confidential Computing attestation API
- Linux IMA-evm-utils for local appraisal

**IMA appraisal** (requires signing all binaries with `evmctl`):
```bash
# Sign a binary for IMA appraisal
evmctl ima_sign --key /etc/keys/ima-signing-key.pem /usr/local/bin/bonnie
```

Appraisal is enabled in the policy but requires all binaries to carry IMA
xattr signatures. Enable gradually: start with measure-only, then add appraise.

---

## 9. Capability Model

Each service in `bonnie.yml` declares exactly the capabilities it needs.
The kernel grants no capabilities beyond what is declared.

| Service | Key capabilities | Rationale |
|---------|-----------------|-----------|
| containerd | SYS_ADMIN, NET_ADMIN, SYS_PTRACE | overlay mounts, network namespaces, exec probing |
| nydus-snapshotter | SYS_ADMIN, MKNOD | FUSE mount |
| stargz-snapshotter | SYS_ADMIN, MKNOD | overlay mounts |
| buildkitd | SYS_ADMIN, NET_ADMIN | overlay mounts, network namespaces |
| bonnie | NET_ADMIN, SETUID, SETGID, CHOWN | job dispatch, file ownership |
| node-exporter | SYS_PTRACE, DAC_READ_SEARCH | /proc reading |
| getty | CHOWN, FOWNER, SETUID, SETGID, TTY | terminal management |

**To verify a running container's capabilities:**
```bash
cat /proc/<container-pid>/status | grep -E "Cap(Inh|Prm|Eff|Bnd|Amb)"
# Use capsh to decode:
capsh --decode=<hex-value>
```

---

## 10. Incident Response

### Kernel panic

The image is configured with `panic=1` — it reboots 1 second after a kernel
panic. In CI, this is correct behaviour (fast recycling). For debugging:
- Set `panic=0` on the cmdline to halt instead of reboot.
- Capture the serial console output via `make smoke` logs.

### OOM kill

```bash
# Check for OOM kills
grep -i "oom\|killed" /var/log/messages
dmesg | grep -i "oom\|killed"
# Or via sysctl metric:
sysctl vm.stat | grep oom_kill
```

Increase the tmpfs size for the affected service or reduce `BONNIE_MAX_PARALLEL_JOBS`.

### AppArmor denial

```bash
# Find denials
dmesg | grep -i "apparmor.*denied"
ausearch -m AVC 2>/dev/null
# Profile is in /etc/apparmor.d/<service>
# Temporarily switch to complain mode to identify needed rules:
aa-complain /etc/apparmor.d/bonnie
# Re-enforce after fixing:
aa-enforce /etc/apparmor.d/bonnie
```

### seccomp violation

```bash
# Seccomp kills appear as SIGSYS (signal 31):
dmesg | grep "SIGSYS\|seccomp"
# strace the process to find which syscall is blocked:
strace -f -e trace=all <command> 2>&1 | grep "EPERM\|ENOSYS"
```

Add the required syscall to `runtime/seccomp-bonnie.json` after verifying
it is safe for the service's threat model.

### Suspicious process

```bash
# List all processes with capabilities
ps -eo pid,cmd,comm | while read pid cmd comm; do
  caps=$(cat /proc/$pid/status 2>/dev/null | grep CapEff | awk '{print $2}')
  [ -n "$caps" ] && [ "$caps" != "0000000000000000" ] \
    && echo "$pid $comm $caps ($cmd)"
done

# Check for unexpected listening ports
ss -tlnp

# Check for unexpected bind mounts
findmnt -t bind
```
