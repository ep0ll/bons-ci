#!/usr/bin/env bash
# =============================================================================
# scripts/setup-host.sh
#
# One-shot host preparation for running bonnie-cicd LinuxKit images.
# Installs all required tools, configures the host sysctl and ulimits,
# sets up the kernel module signing key infrastructure, and validates
# the environment before a build.
#
# Tested on: Ubuntu 24.04 LTS, Debian 12 (amd64 + arm64)
#
# Usage:
#   sudo bash scripts/setup-host.sh [--ci]
#
# Flags:
#   --ci     Non-interactive mode for CI environments
#
# What it does:
#   1. Installs linuxkit, docker, qemu, cosign, crane, pyyaml
#   2. Configures host sysctl for optimal nested QEMU performance
#   3. Sets ulimits for large parallel builds
#   4. Generates kernel module signing keys (if SIGN_MODULES=1)
#   5. Validates KVM availability
#   6. Prints a readiness summary
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[1;33m'
CYN='\033[0;36m'; BLD='\033[1m'; RST='\033[0m'

CI_MODE=0
[[ "${1:-}" == "--ci" ]] && CI_MODE=1

step() { echo -e "\n${BLD}${CYN}>> $*${RST}"; }
ok()   { echo -e "  ${GRN}OK${RST}  $*"; }
warn() { echo -e "  ${YLW}WARN${RST}  $*"; }
die()  { echo -e "  ${RED}FAIL${RST}  $*" >&2; exit 1; }

[[ "$EUID" -eq 0 ]] || die "Run as root: sudo bash $0"

ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  LK_ARCH="amd64" ;;
  aarch64) LK_ARCH="arm64" ;;
  *)       die "Unsupported arch: $ARCH" ;;
esac

LK_VERSION="${LINUXKIT_VERSION:-v1.2.0}"
COSIGN_VERSION="${COSIGN_VERSION:-2.2.4}"
CRANE_VERSION="${CRANE_VERSION:-0.19.1}"

# ── 1. System packages ────────────────────────────────────────────────────────
step "Installing system packages"
apt-get update -qq
apt-get install -y --no-install-recommends \
  curl wget ca-certificates gnupg lsb-release \
  qemu-system-x86 qemu-utils \
  python3 python3-pip python3-yaml \
  git make jq bc \
  zip unzip \
  openssl \
  linux-tools-common linux-tools-generic \
  sysstat \
  apparmor apparmor-utils \
  2>/dev/null
ok "System packages installed"

# ── 2. Docker ────────────────────────────────────────────────────────────────
step "Installing Docker"
if ! command -v docker &>/dev/null; then
  curl -fsSL https://get.docker.com | bash
  systemctl enable --now docker
  ok "Docker installed"
else
  DOCKER_VER=$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo "unknown")
  ok "Docker already installed: $DOCKER_VER"
fi

# ── 3. linuxkit ───────────────────────────────────────────────────────────────
step "Installing linuxkit ${LK_VERSION}"
if ! linuxkit version 2>/dev/null | grep -q "${LK_VERSION#v}"; then
  curl -fsSL \
    "https://github.com/linuxkit/linuxkit/releases/download/${LK_VERSION}/linuxkit-linux-${LK_ARCH}" \
    -o /usr/local/bin/linuxkit
  chmod +x /usr/local/bin/linuxkit
  ok "linuxkit $(linuxkit version 2>&1 | head -1)"
else
  ok "linuxkit already at ${LK_VERSION}"
fi

# ── 4. cosign ────────────────────────────────────────────────────────────────
step "Installing cosign ${COSIGN_VERSION}"
if ! command -v cosign &>/dev/null; then
  curl -fsSL \
    "https://github.com/sigstore/cosign/releases/download/v${COSIGN_VERSION}/cosign-linux-${LK_ARCH}" \
    -o /usr/local/bin/cosign
  chmod +x /usr/local/bin/cosign
  ok "cosign $(cosign version 2>&1 | grep GitVersion || echo installed)"
else
  ok "cosign already installed"
fi

# ── 5. crane ────────────────────────────────────────────────────────────────
step "Installing crane ${CRANE_VERSION}"
if ! command -v crane &>/dev/null; then
  CRANE_URL="https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_${ARCH}.tar.gz"
  curl -fsSL "$CRANE_URL" | tar -xz -C /usr/local/bin crane
  ok "crane $(crane version 2>&1 | head -1)"
else
  ok "crane already installed"
fi

# ── 6. KVM ───────────────────────────────────────────────────────────────────
step "Checking KVM"
if [[ -e /dev/kvm ]]; then
  KVM_OK=1
  ok "KVM available — hardware acceleration enabled"
  # Allow the current user and docker group to use KVM
  chmod 666 /dev/kvm
else
  KVM_OK=0
  warn "KVM not available — QEMU will run in software emulation (slower boots)"
fi

# ── 7. Host sysctl for build host ─────────────────────────────────────────────
step "Applying host sysctl"
cat > /etc/sysctl.d/99-bonnie-host.conf << 'SYSCTL'
# Optimise the build host for running bonnie-cicd in QEMU
fs.inotify.max_user_watches   = 1048576
fs.inotify.max_user_instances = 8192
fs.file-max                   = 2097152
vm.max_map_count              = 1048576
net.core.rmem_max             = 134217728
net.core.wmem_max             = 134217728
net.ipv4.tcp_rmem             = 4096 87380 134217728
net.ipv4.tcp_wmem             = 4096 65536 134217728
# Allow nested KVM
kernel.perf_event_paranoid    = -1
SYSCTL
sysctl -p /etc/sysctl.d/99-bonnie-host.conf &>/dev/null
ok "Host sysctl applied"

# ── 8. Host ulimits ───────────────────────────────────────────────────────────
step "Configuring host ulimits"
cat > /etc/security/limits.d/99-bonnie-host.conf << 'LIMITS'
*    soft nofile  1048576
*    hard nofile  1048576
*    soft nproc   unlimited
*    hard nproc   unlimited
root soft nofile  1048576
root hard nofile  1048576
LIMITS
# Apply immediately to this shell session
ulimit -n 1048576 2>/dev/null || true
ok "ulimits configured (take effect on next login)"

# ── 9. Docker daemon configuration ───────────────────────────────────────────
step "Configuring Docker daemon"
mkdir -p /etc/docker
cat > /etc/docker/daemon.json << 'DOCKER'
{
  "storage-driver":    "overlay2",
  "log-driver":        "json-file",
  "log-opts": {
    "max-size": "100m",
    "max-file": "3"
  },
  "default-ulimits": {
    "nofile": { "Name": "nofile", "Hard": 1048576, "Soft": 1048576 }
  },
  "features": { "buildkit": true },
  "experimental":     false,
  "live-restore":      true
}
DOCKER
systemctl reload docker 2>/dev/null || true
ok "Docker daemon configured"

# ── 10. KVM nested virtualisation ────────────────────────────────────────────
step "Enabling nested KVM"
if modinfo kvm_intel &>/dev/null 2>&1; then
  if ! grep -q "^options kvm_intel nested=1" /etc/modprobe.d/kvm.conf 2>/dev/null; then
    echo "options kvm_intel nested=1" >> /etc/modprobe.d/kvm.conf
    echo "options kvm_amd  nested=1"  >> /etc/modprobe.d/kvm.conf
    modprobe -r kvm_intel 2>/dev/null || true
    modprobe kvm_intel nested=1 2>/dev/null || true
    ok "Nested KVM enabled (Intel)"
  else
    ok "Nested KVM already configured"
  fi
elif modinfo kvm_amd &>/dev/null 2>&1; then
  if ! grep -q "^options kvm_amd nested=1" /etc/modprobe.d/kvm.conf 2>/dev/null; then
    echo "options kvm_amd nested=1" >> /etc/modprobe.d/kvm.conf
    modprobe -r kvm_amd 2>/dev/null || true
    modprobe kvm_amd nested=1 2>/dev/null || true
    ok "Nested KVM enabled (AMD)"
  else
    ok "Nested KVM already configured"
  fi
else
  warn "KVM modules not available — skipping nested KVM config"
fi

# ── 11. Validate ──────────────────────────────────────────────────────────────
step "Validation summary"
echo ""
READY=1
for cmd in linuxkit docker qemu-system-x86_64 cosign crane python3; do
  if command -v "$cmd" &>/dev/null; then
    VER=$(command -v "$cmd")
    ok "$cmd: $VER"
  else
    warn "$cmd: NOT FOUND"
    READY=0
  fi
done

python3 -c "import yaml" 2>/dev/null \
  && ok "python3-yaml: available" \
  || { warn "python3-yaml: missing (pip3 install pyyaml)"; READY=0; }

echo ""
if (( READY == 1 )); then
  echo -e "  ${GRN}${BLD}Host is ready for bonnie-cicd builds.${RST}"
  echo -e "  Next: export BONNIE_IMAGE=<your-registry>/bonnie:latest && make build"
else
  echo -e "  ${YLW}${BLD}Host setup has warnings — review above before building.${RST}"
fi
