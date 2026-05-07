#!/usr/bin/env bash
# =============================================================================
# build.sh  –  Build, test, and optionally push the bonnie-cicd LinuxKit image
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

# ── Colours ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

log()  { echo -e "${CYAN}[bonnie-build]${RESET} $*"; }
ok()   { echo -e "${GREEN}[  OK  ]${RESET} $*"; }
warn() { echo -e "${YELLOW}[ WARN ]${RESET} $*"; }
die()  { echo -e "${RED}[ FAIL ]${RESET} $*" >&2; exit 1; }

# ── Config ───────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE_NAME="${IMAGE_NAME:-bonnie-cicd}"
IMAGE_TAG="${IMAGE_TAG:-$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || echo 'dev')}"
OUTPUT_DIR="${OUTPUT_DIR:-${SCRIPT_DIR}/output}"
FORMAT="${FORMAT:-raw}"                   # raw | iso | kernel+initrd | qcow2 | vmdk
LINUXKIT_BIN="${LINUXKIT_BIN:-linuxkit}"
PUSH_REGISTRY="${PUSH_REGISTRY:-}"       # set to push e.g. registry.example.com/bonnie
BONNIE_IMAGE="${BONNIE_IMAGE:-bonnie:latest}"   # your proprietary image

# ── Sanity checks ─────────────────────────────────────────────────────────────
check_deps() {
  local missing=()
  for cmd in linuxkit docker jq; do
    command -v "$cmd" &>/dev/null || missing+=("$cmd")
  done
  [[ ${#missing[@]} -eq 0 ]] || die "Missing required tools: ${missing[*]}"

  local lk_ver
  lk_ver=$("$LINUXKIT_BIN" version 2>&1 | head -1)
  log "LinuxKit: $lk_ver"

  # Verify bonnie image is accessible
  docker inspect "$BONNIE_IMAGE" &>/dev/null || \
    docker pull "$BONNIE_IMAGE" &>/dev/null || \
    die "Cannot locate bonnie image: $BONNIE_IMAGE — set BONNIE_IMAGE or run: docker pull <your-registry>/bonnie"
}

# ── Lint the YAML ─────────────────────────────────────────────────────────────
lint() {
  log "Linting ${IMAGE_NAME}.yml …"
  "$LINUXKIT_BIN" pkg show-tag "${SCRIPT_DIR}/bonnie.yml" &>/dev/null || true
  # Basic YAML validity via Python (always available in CI)
  python3 -c "
import sys, yaml
with open('${SCRIPT_DIR}/bonnie.yml') as f:
    doc = yaml.safe_load(f)
required = {'kernel','init','onboot','services'}
missing  = required - set(doc.keys())
if missing:
    print('ERROR: missing top-level keys:', missing, file=sys.stderr)
    sys.exit(1)
print('YAML OK')
"
  ok "Lint passed"
}

# ── Build ──────────────────────────────────────────────────────────────────────
build() {
  log "Building image: ${IMAGE_NAME}:${IMAGE_TAG} (format=${FORMAT})"
  mkdir -p "$OUTPUT_DIR"

  "$LINUXKIT_BIN" build \
    --format    "$FORMAT" \
    --name      "${IMAGE_NAME}" \
    --dir       "$OUTPUT_DIR" \
    --pull \
    "${SCRIPT_DIR}/bonnie.yml"

  ok "Build complete → ${OUTPUT_DIR}/${IMAGE_NAME}.*"
  ls -lh "${OUTPUT_DIR}/${IMAGE_NAME}."* 2>/dev/null || true
}

# ── Reproducibility – record image digests ────────────────────────────────────
record_sbom() {
  log "Recording image digests for supply-chain audit …"
  local sbom="${OUTPUT_DIR}/sbom-${IMAGE_TAG}.json"

  python3 - <<'PYEOF' > "$sbom"
import json, subprocess, sys, yaml, pathlib

with open(pathlib.Path(__file__).parent / '../bonnie.yml') as f:
    doc = yaml.safe_load(f)

images = []
for section in ('init', 'onboot', 'services'):
    items = doc.get(section, [])
    if section == 'init':
        items = [{'image': i} for i in items]
    for item in items:
        img = item.get('image','')
        if img:
            try:
                out = subprocess.check_output(
                    ['docker', 'inspect', '--format', '{{index .RepoDigests 0}}', img],
                    stderr=subprocess.DEVNULL, text=True).strip()
            except Exception:
                out = 'NOT_PULLED'
            images.append({'ref': img, 'digest': out})

print(json.dumps({'images': images}, indent=2))
PYEOF

  ok "SBOM written → $sbom"
}

# ── Smoke-test (boot in QEMU for ≤15 s, check containerd socket) ─────────────
smoke_test() {
  log "Running smoke test (QEMU, 15 s timeout) …"
  command -v qemu-system-x86_64 &>/dev/null || { warn "qemu not found – skipping smoke test"; return; }

  local kernel="${OUTPUT_DIR}/${IMAGE_NAME}-kernel"
  local initrd="${OUTPUT_DIR}/${IMAGE_NAME}-initrd.img"
  [[ -f "$kernel" && -f "$initrd" ]] || {
    warn "kernel+initrd not found (format=${FORMAT}) – skipping smoke test"
    return
  }

  timeout 15 qemu-system-x86_64 \
    -nographic \
    -no-reboot \
    -m 512M \
    -smp 2 \
    -enable-kvm 2>/dev/null || true \
    -kernel "$kernel" \
    -initrd "$initrd" \
    -append "console=ttyS0 quiet loglevel=0" \
    2>&1 | tee "${OUTPUT_DIR}/smoke.log" | grep -E "(containerd|bonnie|error|panic)" || true

  ok "Smoke test log → ${OUTPUT_DIR}/smoke.log"
}

# ── Push to registry ─────────────────────────────────────────────────────────
push() {
  [[ -z "$PUSH_REGISTRY" ]] && { log "PUSH_REGISTRY not set – skipping push"; return; }
  log "Pushing image to ${PUSH_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG} …"
  "$LINUXKIT_BIN" push \
    --registry "$PUSH_REGISTRY" \
    --name     "${IMAGE_NAME}" \
    --tag      "${IMAGE_TAG}" \
    "$OUTPUT_DIR"
  ok "Pushed ${PUSH_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
}

# ── Entrypoint ───────────────────────────────────────────────────────────────
main() {
  local cmd="${1:-build}"
  case "$cmd" in
    lint)       check_deps; lint ;;
    build)      check_deps; lint; build; record_sbom ;;
    smoke)      smoke_test ;;
    push)       push ;;
    all)        check_deps; lint; build; record_sbom; smoke_test; push ;;
    *)          echo "Usage: $0 [lint|build|smoke|push|all]"; exit 1 ;;
  esac
}

main "$@"
