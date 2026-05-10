#!/usr/bin/env bash
# =============================================================================
# scripts/build-kernel.sh
#
# Builds a custom hardened Linux kernel for bonnie-cicd using the Kconfig
# fragment in kernel-config-fragments/bonnie-cicd.config, then packages it
# as a linuxkit kernel pkg and updates bonnie.yml with the new digest.
#
# The build runs inside a pinned Docker container for full reproducibility.
# Output is pushed to the registry specified by KERNEL_REGISTRY.
#
# Usage:
#   bash scripts/build-kernel.sh
#
# Required env:
#   KERNEL_REGISTRY   registry to push the kernel pkg  (e.g. ghcr.io/my-org)
#   KERNEL_VERSION    Linux version to build            (default: 6.6.22)
#
# Optional env:
#   KERNEL_ORG        org prefix for linuxkit pkg name  (default: $KERNEL_REGISTRY)
#   BUILD_JOBS        parallel make jobs                (default: nproc)
#   PUSH              set to 1 to push after build      (default: 0)
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[1;33m'
CYN='\033[0;36m'; BLD='\033[1m'; RST='\033[0m'

step() { echo -e "\n${BLD}${CYN}>> $*${RST}"; }
ok()   { echo -e "  ${GRN}OK${RST}  $*"; }
warn() { echo -e "  ${YLW}WARN${RST}  $*"; }
die()  { echo -e "  ${RED}FAIL${RST}  $*" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
KFRAG="${ROOT_DIR}/kernel-config-fragments/bonnie-cicd.config"

KERNEL_VERSION="${KERNEL_VERSION:-6.6.22}"
KERNEL_REGISTRY="${KERNEL_REGISTRY:-}"
KERNEL_ORG="${KERNEL_ORG:-${KERNEL_REGISTRY}}"
BUILD_JOBS="${BUILD_JOBS:-$(nproc)}"
PUSH="${PUSH:-0}"
WORK_DIR="${WORK_DIR:-/tmp/bonnie-kernel-build}"

[[ -n "$KERNEL_REGISTRY" ]] || die "Set KERNEL_REGISTRY=<host/org>"
[[ -f "$KFRAG" ]] || die "Kconfig fragment not found: $KFRAG"

# Pin the build container for reproducibility
BUILD_IMAGE="linuxkit/kernel-build:6.6.22@sha256:a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
KERNEL_PKG="${KERNEL_REGISTRY}/kernel:${KERNEL_VERSION}-bonnie"

step "Preparing build dir: $WORK_DIR"
mkdir -p "$WORK_DIR"

# ── Download kernel source ────────────────────────────────────────────────────
TARBALL="${WORK_DIR}/linux-${KERNEL_VERSION}.tar.xz"
if [[ ! -f "$TARBALL" ]]; then
  step "Downloading Linux ${KERNEL_VERSION}"
  curl -fsSL \
    "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz" \
    -o "$TARBALL"
  # Verify against published SHA-256
  curl -fsSL \
    "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz.sha256" \
    -o "${TARBALL}.sha256"
  sha256sum -c "${TARBALL}.sha256" || die "Kernel tarball checksum mismatch"
  ok "Download verified"
else
  ok "Using cached tarball: $TARBALL"
fi

# ── Extract ───────────────────────────────────────────────────────────────────
SRC_DIR="${WORK_DIR}/linux-${KERNEL_VERSION}"
if [[ ! -d "$SRC_DIR" ]]; then
  step "Extracting kernel source"
  tar -xf "$TARBALL" -C "$WORK_DIR"
  ok "Extracted to $SRC_DIR"
fi

# ── Merge Kconfig fragment ────────────────────────────────────────────────────
step "Merging Kconfig fragment"
cp "$KFRAG" "${SRC_DIR}/bonnie-cicd.config"

docker run --rm \
  -v "${SRC_DIR}:/linux" \
  -w /linux \
  "$BUILD_IMAGE" \
  bash -c "
    make x86_64_defconfig
    scripts/kconfig/merge_config.sh \
      -m arch/x86/configs/x86_64_defconfig \
      bonnie-cicd.config
    make olddefconfig
  "

ok "Config merged. Verifying key options:"
for opt in \
  CONFIG_PREEMPT_NONE CONFIG_HZ_100 \
  CONFIG_TCP_CONG_BBR CONFIG_NET_SCH_FQ \
  CONFIG_IO_URING CONFIG_OVERLAY_FS \
  CONFIG_FUSE_FS CONFIG_SECURITY_APPARMOR \
  CONFIG_SECURITY_LANDLOCK CONFIG_BPF_JIT_ALWAYS_ON \
  CONFIG_PAGE_TABLE_ISOLATION CONFIG_RETPOLINE \
  CONFIG_INIT_ON_FREE_DEFAULT_ON CONFIG_MODULE_SIG_FORCE \
  CONFIG_RD_ZSTD CONFIG_KERNEL_ZSTD \
  CONFIG_KVM CONFIG_VIRTIO_NET; do
  val=$(grep -E "^${opt}=" "${SRC_DIR}/.config" 2>/dev/null | head -1 || true)
  if [[ -n "$val" ]]; then
    ok "  $val"
  else
    warn "  $opt not set — check fragment"
  fi
done

# ── Compile ───────────────────────────────────────────────────────────────────
step "Compiling kernel (jobs=${BUILD_JOBS}) — this takes ~10 min on 16 cores"
time docker run --rm \
  -v "${SRC_DIR}:/linux" \
  -w /linux \
  -e MAKEFLAGS="-j${BUILD_JOBS}" \
  "$BUILD_IMAGE" \
  bash -c "
    make -j${BUILD_JOBS} bzImage
    make -j${BUILD_JOBS} modules
    make INSTALL_MOD_PATH=/linux/_modules modules_install
  "
ok "Kernel compiled"

# ── Package as linuxkit kernel pkg ───────────────────────────────────────────
step "Packaging as linuxkit kernel pkg: $KERNEL_PKG"

PKG_DIR="${WORK_DIR}/kernel-pkg"
mkdir -p "${PKG_DIR}"

cp "${SRC_DIR}/arch/x86/boot/bzImage" "${PKG_DIR}/kernel"
cp "${SRC_DIR}/System.map"            "${PKG_DIR}/System.map"
cp "${SRC_DIR}/.config"               "${PKG_DIR}/config"

# Create minimal linuxkit pkg Dockerfile
cat > "${PKG_DIR}/Dockerfile" << PKGEOF
FROM scratch
COPY kernel    /kernel
COPY System.map /System.map
COPY config    /config
PKGEOF

# Build and optionally push
docker build -t "${KERNEL_PKG}" "${PKG_DIR}"
ok "Kernel image built: $KERNEL_PKG"

if [[ "$PUSH" == "1" ]]; then
  step "Pushing $KERNEL_PKG"
  docker push "$KERNEL_PKG"
  DIGEST=$(docker inspect --format '{{index .RepoDigests 0}}' "$KERNEL_PKG")
  ok "Pushed: $DIGEST"

  # Patch bonnie.yml with new kernel digest
  step "Updating bonnie.yml kernel.image"
  sed -i "s|image: linuxkit/kernel:.*|image: ${DIGEST}|" "${ROOT_DIR}/bonnie.yml"
  ok "bonnie.yml updated"
else
  warn "PUSH=0 — skipping push. Set PUSH=1 to push and update bonnie.yml."
  DIGEST=$(docker inspect --format '{{.Id}}' "$KERNEL_PKG")
  echo "  Local image ID: $DIGEST"
fi

step "Done"
echo -e "  Kernel pkg : ${BLD}${KERNEL_PKG}${RST}"
echo -e "  bzImage    : ${SRC_DIR}/arch/x86/boot/bzImage"
echo -e "  .config    : ${SRC_DIR}/.config"
echo -e "  Work dir   : ${WORK_DIR}"
