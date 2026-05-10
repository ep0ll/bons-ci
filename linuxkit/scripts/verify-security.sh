#!/usr/bin/env bash
# =============================================================================
# scripts/verify-security.sh
#
# Verifies the security posture of a running bonnie-cicd instance.
# Run inside the VM (via getty or SSH) or piped through linuxkit exec.
#
# Exit codes:
#   0 = all checks PASS
#   1 = at least one FAIL
#   2 = warnings only, no failures
#
# Usage (inside VM):
#   bash /etc/bonnie/verify-security.sh
#
# Usage (from host via linuxkit):
#   linuxkit run qemu \
#     --kernel output/bonnie-cicd-kernel \
#     --initrd output/bonnie-cicd-initrd.img -- \
#     bash /etc/bonnie/verify-security.sh
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

GRN='\033[0;32m'; RED='\033[0;31m'; YLW='\033[1;33m'
BLD='\033[1m'; CYN='\033[0;36m'; RST='\033[0m'

FAILS=0
WARNS=0

pass() { echo -e "  ${GRN}PASS${RST}  $*"; }
fail() { echo -e "  ${RED}FAIL${RST}  $*"; (( FAILS++ )) || true; }
warn() { echo -e "  ${YLW}WARN${RST}  $*"; (( WARNS++ )) || true; }
sec()  { echo -e "\n${BLD}${CYN}-- $* --${RST}"; }

sysctl_val() { sysctl -n "$1" 2>/dev/null || echo "MISSING"; }
check_sysctl() {
  local key="$1" want="$2" got
  got=$(sysctl_val "$key")
  [[ "$got" == "$want" ]] \
    && pass "sysctl ${key} = ${want}" \
    || fail "sysctl ${key} = ${got}  (want ${want})"
}

# =============================================================================
sec "Kernel"
# =============================================================================

KVER=$(uname -r)
echo -e "  Kernel: ${BLD}${KVER}${RST}"
[[ "$KVER" == 6.6* ]] && pass "Kernel 6.6 LTS" || warn "Unexpected kernel: $KVER"

grep -q "nokaslr" /proc/cmdline \
  && fail "KASLR disabled on cmdline" \
  || pass "KASLR enabled"

grep -q "vsyscall=none" /proc/cmdline \
  && pass "vsyscall=none" \
  || fail "vsyscall not disabled"

grep -q "debugfs=off" /proc/cmdline \
  && pass "debugfs=off" \
  || warn "debugfs not explicitly disabled"

grep -q "init_on_free=1" /proc/cmdline \
  && pass "init_on_free=1" \
  || fail "init_on_free not set on cmdline"

grep -q "init_on_alloc=1" /proc/cmdline \
  && pass "init_on_alloc=1" \
  || fail "init_on_alloc not set on cmdline"

grep -q "slab_nomerge" /proc/cmdline \
  && pass "slab_nomerge" \
  || fail "slab_nomerge not set"

grep -q "page_poison=1" /proc/cmdline \
  && pass "page_poison=1" \
  || warn "page_poison not set"

if [[ -f /sys/kernel/security/lockdown ]]; then
  LOCKDOWN=$(cat /sys/kernel/security/lockdown)
  [[ "$LOCKDOWN" == *"integrity"* ]] \
    && pass "Kernel lockdown=integrity" \
    || fail "Kernel lockdown not integrity: $LOCKDOWN"
else
  fail "Kernel lockdown unavailable"
fi

if grep -q "CONFIG_MODULE_SIG_FORCE=y" /boot/config-"$(uname -r)" 2>/dev/null; then
  pass "Module signature enforcement (from boot config)"
else
  warn "Cannot verify module signing (no /boot/config-$(uname -r))"
fi

MODDIS=$(sysctl_val kernel.modules_disabled)
[[ "$MODDIS" == "1" ]] \
  && pass "kernel.modules_disabled=1" \
  || warn "kernel.modules_disabled=${MODDIS} (set=1 post-boot for max lockdown)"

# =============================================================================
sec "Security Sysctls"
# =============================================================================

check_sysctl "kernel.kptr_restrict"             "2"
check_sysctl "kernel.dmesg_restrict"            "1"
check_sysctl "kernel.perf_event_paranoid"       "3"
check_sysctl "kernel.unprivileged_bpf_disabled" "1"
check_sysctl "net.core.bpf_jit_harden"         "2"
check_sysctl "kernel.yama.ptrace_scope"         "2"
check_sysctl "kernel.randomize_va_space"        "2"
check_sysctl "kernel.sysrq"                     "0"
check_sysctl "fs.suid_dumpable"                 "0"
check_sysctl "net.ipv4.conf.all.accept_redirects"    "0"
check_sysctl "net.ipv4.conf.all.send_redirects"      "0"
check_sysctl "net.ipv4.conf.all.accept_source_route" "0"
check_sysctl "net.ipv4.conf.all.rp_filter"           "1"
check_sysctl "net.ipv4.tcp_syncookies"               "1"
check_sysctl "net.ipv4.icmp_echo_ignore_broadcasts"  "1"
check_sysctl "net.ipv6.conf.all.accept_ra"           "0"
check_sysctl "net.ipv6.conf.all.accept_redirects"    "0"

# =============================================================================
sec "Network Performance"
# =============================================================================

check_sysctl "net.core.default_qdisc"          "fq"
check_sysctl "net.ipv4.tcp_congestion_control" "bbr"
check_sysctl "net.ipv4.tcp_slow_start_after_idle" "0"
check_sysctl "net.ipv4.tcp_fastopen"           "3"

# =============================================================================
sec "AppArmor"
# =============================================================================

AA_STATUS=/sys/module/apparmor/parameters/enabled
if [[ -f "$AA_STATUS" ]] && [[ "$(cat "$AA_STATUS")" == "Y" ]]; then
  pass "AppArmor kernel module loaded"
else
  fail "AppArmor not enabled"
fi

if command -v aa-status &>/dev/null; then
  ENFORCED=$(aa-status --enforced 2>/dev/null || echo 0)
  COMPLAINING=$(aa-status --complaining 2>/dev/null || echo 0)
  (( ENFORCED > 0 )) \
    && pass "AppArmor profiles enforced: ${ENFORCED}" \
    || warn "No AppArmor profiles in enforce mode (complaining: ${COMPLAINING})"
else
  warn "aa-status not available; cannot enumerate profiles"
fi

# =============================================================================
sec "cgroups"
# =============================================================================

mount | grep -q "cgroup2" \
  && pass "cgroup v2 mounted" \
  || fail "cgroup v2 not mounted"

mount | grep -q " type cgroup " \
  && fail "cgroup v1 hierarchy detected (should be disabled)" \
  || pass "No cgroup v1 hierarchy"

# =============================================================================
sec "Filesystem"
# =============================================================================

mount | grep -E " / .*\bro\b" &>/dev/null \
  && pass "Root filesystem is read-only" \
  || fail "Root filesystem is NOT read-only"

for dir in /tmp /run /var/lib; do
  mount | grep -q "tmpfs on ${dir}" \
    && pass "${dir} on tmpfs (ephemeral)" \
    || warn "${dir} not on tmpfs"
done

mount | grep "proc on /proc" | grep -q "nosuid" \
  && pass "/proc mounted with nosuid" \
  || fail "/proc not mounted with nosuid"

mount | grep "tmpfs on /tmp" | grep -q "noexec" \
  && pass "/tmp mounted with noexec" \
  || warn "/tmp not mounted with noexec"

# =============================================================================
sec "Open Ports (expected: 1338 9090 9091 9092 9100)"
# =============================================================================

EXPECTED="1338 9090 9091 9092 9100"
LISTENING=$(ss -tlnH 2>/dev/null | awk '{print $4}' | awk -F: '{print $NF}' | sort -un)
UNEXPECTED=()
for port in $LISTENING; do
  found=0
  for exp in $EXPECTED; do [[ "$port" == "$exp" ]] && found=1 && break; done
  (( found == 0 )) && UNEXPECTED+=("$port")
done
(( ${#UNEXPECTED[@]} == 0 )) \
  && pass "No unexpected listening ports" \
  || warn "Unexpected listening ports: ${UNEXPECTED[*]}"

# =============================================================================
sec "Service Sockets"
# =============================================================================

check_sock() {
  [[ -S "$2" ]] \
    && pass "$1 socket: $2" \
    || fail "$1 socket missing: $2"
}

check_sock "containerd"         "/run/containerd/containerd.sock"
check_sock "nydus-snapshotter"  "/run/containerd-nydus/containerd-nydus-grpc.sock"
check_sock "stargz-snapshotter" "/run/containerd-stargz-grpc/containerd-stargz-grpc.sock"
check_sock "buildkitd"          "/run/buildkit/buildkitd.sock"
check_sock "bonnie"             "/run/bonnie/bonnie.sock"

# =============================================================================
sec "IMA / Integrity"
# =============================================================================

if [[ -f /sys/kernel/security/ima/ascii_runtime_measurements ]]; then
  MCOUNT=$(wc -l < /sys/kernel/security/ima/ascii_runtime_measurements)
  pass "IMA active -- ${MCOUNT} measurements recorded"
else
  warn "IMA measurements file not found (may not be configured)"
fi

# =============================================================================
sec "Huge Pages"
# =============================================================================

HPAGES=$(sysctl_val vm.nr_hugepages)
(( HPAGES >= 512 )) \
  && pass "Huge pages allocated: ${HPAGES}" \
  || warn "Huge pages: ${HPAGES} (expected >= 512)"

# =============================================================================
sec "ulimits"
# =============================================================================

NOFILE=$(ulimit -n 2>/dev/null || echo 0)
(( NOFILE >= 1048576 )) \
  && pass "nofile limit: ${NOFILE}" \
  || warn "nofile limit ${NOFILE} < 1048576 (check limits.d)"

# =============================================================================
sec "Result"
# =============================================================================

echo ""
if   (( FAILS > 0 )); then
  echo -e "  ${RED}${BLD}FAILED${RST} -- ${FAILS} failure(s), ${WARNS} warning(s)"
  exit 1
elif (( WARNS > 0 )); then
  echo -e "  ${YLW}${BLD}WARNINGS${RST} -- 0 failures, ${WARNS} warning(s)"
  exit 2
else
  echo -e "  ${GRN}${BLD}ALL CHECKS PASSED${RST} -- security posture is nominal"
  exit 0
fi
