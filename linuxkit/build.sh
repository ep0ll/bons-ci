#!/usr/bin/env bash
# =============================================================================
# build.sh — bonnie-cicd LinuxKit image builder
# Usage: build.sh [lint|build|smoke|sign|push|all]
#
# Env:
#   BONNIE_IMAGE      your-registry/bonnie:tag    (required for build)
#   FORMAT            raw|iso|qcow2|vmdk|kernel+initrd  (default: raw)
#   OUTPUT_DIR        path for artefacts           (default: ./output)
#   PUSH_REGISTRY     registry host                (default: none)
#   IMAGE_TAG         override auto-detected git tag
#   LINUXKIT_BIN      path to linuxkit binary      (default: linuxkit)
#   SMOKE_TIMEOUT     seconds for smoke test        (default: 20)
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[1;33m'
CYN='\033[0;36m'; BLD='\033[1m'; RST='\033[0m'

step() { echo -e "\n${BLD}${CYN}>> $*${RST}"; }
ok()   { echo -e "  ${GRN}OK${RST}  $*"; }
warn() { echo -e "  ${YLW}WARN${RST}  $*"; }
die()  { echo -e "  ${RED}FAIL${RST}  $*" >&2; exit 1; }

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BONNIE_IMAGE="${BONNIE_IMAGE:-bonnie:latest}"
FORMAT="${FORMAT:-raw}"
OUTPUT_DIR="${OUTPUT_DIR:-${DIR}/output}"
PUSH_REGISTRY="${PUSH_REGISTRY:-}"
IMAGE_NAME="bonnie-cicd"
IMAGE_TAG="${IMAGE_TAG:-$(git -C "$DIR" describe --tags --always --dirty 2>/dev/null || echo dev)}"
LINUXKIT_BIN="${LINUXKIT_BIN:-linuxkit}"
SMOKE_TIMEOUT="${SMOKE_TIMEOUT:-20}"

check_deps() {
  step "Checking dependencies"
  local missing=()
  for cmd in "$LINUXKIT_BIN" docker python3; do
    command -v "$cmd" &>/dev/null && ok "$cmd" || missing+=("$cmd")
  done
  [[ ${#missing[@]} -eq 0 ]] || die "Missing: ${missing[*]}"
  python3 -c "import yaml" 2>/dev/null \
    || die "python3-yaml not installed (pip3 install pyyaml)"
  step "Checking bonnie image: $BONNIE_IMAGE"
  docker inspect "$BONNIE_IMAGE" &>/dev/null \
    || docker pull "$BONNIE_IMAGE" \
    || die "Cannot reach bonnie image '$BONNIE_IMAGE'. Set BONNIE_IMAGE."
  ok "bonnie image OK"
}

lint() {
  step "Linting bonnie.yml"
  python3 - << 'PYEOF'
import sys, yaml, pathlib, re

path = pathlib.Path(__file__).parent / "bonnie.yml"
try:
    doc = yaml.safe_load(path.read_text())
except yaml.YAMLError as e:
    print(f"YAML parse error: {e}", file=sys.stderr); sys.exit(1)

errors = []
required = {"kernel", "init", "onboot", "services", "files"}
for k in required - set(doc.keys()):
    errors.append(f"Missing top-level key: {k}")

for section in ("onboot", "services"):
    for item in doc.get(section, []):
        if not isinstance(item, dict):
            errors.append(f"{section}: non-dict item"); continue
        if "name" not in item:
            errors.append(f"{section}: item missing 'name'")
        if "image" not in item:
            errors.append(f"{section}: '{item.get('name','?')}' missing 'image'")

digest_re = re.compile(r"@sha256:[a-f0-9]{64}")
for section in ("onboot", "services"):
    for item in doc.get(section, []):
        if isinstance(item, dict):
            img = item.get("image", "")
            if img and "bonnie" not in img and not digest_re.search(img):
                print(f"  WARN: '{item.get('name')}' not pinned by digest: {img}")

if errors:
    for e in errors: print(f"  ERROR: {e}", file=sys.stderr)
    sys.exit(1)
print("  YAML valid")
PYEOF
  ok "Lint passed"
}

build() {
  step "Building ${IMAGE_NAME}:${IMAGE_TAG} [format=${FORMAT}]"
  mkdir -p "$OUTPUT_DIR"
  local tmp="${OUTPUT_DIR}/bonnie-resolved.yml"
  sed "s|image: bonnie:latest|image: ${BONNIE_IMAGE}|g" "${DIR}/bonnie.yml" > "$tmp"
  "$LINUXKIT_BIN" build --format "$FORMAT" --name "$IMAGE_NAME" \
    --dir "$OUTPUT_DIR" --pull "$tmp"
  rm -f "$tmp"
  ok "Build complete"
  ls -lh "${OUTPUT_DIR}/${IMAGE_NAME}."* 2>/dev/null || true
}

sbom() {
  step "Generating SBOM"
  python3 - << PYEOF > "${OUTPUT_DIR}/sbom-${IMAGE_TAG}.json"
import json, subprocess, yaml, pathlib, datetime
doc = yaml.safe_load(pathlib.Path("${DIR}/bonnie.yml").read_text())
recs = []
for section in ("init", "onboot", "services"):
    items = doc.get(section, [])
    if section == "init":
        items = [{"name": f"init[{i}]", "image": img} for i, img in enumerate(items)]
    for item in items:
        img = item.get("image", "")
        if not img: continue
        try:
            digest = subprocess.check_output(
                ["docker", "inspect", "--format", "{{index .RepoDigests 0}}", img],
                stderr=subprocess.DEVNULL, text=True).strip()
        except Exception:
            digest = "NOT_PULLED"
        recs.append({"name": item.get("name", ""), "ref": img, "digest": digest})
print(json.dumps({"schema": "bonnie-cicd-sbom/v1",
    "built_at": datetime.datetime.utcnow().isoformat() + "Z",
    "tag": "${IMAGE_TAG}", "images": recs}, indent=2))
PYEOF
  ok "SBOM -> ${OUTPUT_DIR}/sbom-${IMAGE_TAG}.json"
}

smoke() {
  step "Smoke test (QEMU ${SMOKE_TIMEOUT}s)"
  command -v qemu-system-x86_64 &>/dev/null \
    || { warn "qemu-system-x86_64 not found - skipping"; return; }
  local kernel="${OUTPUT_DIR}/${IMAGE_NAME}-kernel"
  local initrd="${OUTPUT_DIR}/${IMAGE_NAME}-initrd.img"
  [[ -f "$kernel" && -f "$initrd" ]] \
    || { warn "kernel/initrd not found - rebuild with FORMAT=kernel+initrd"; return; }
  local log="${OUTPUT_DIR}/smoke-${IMAGE_TAG}.log"
  timeout "$SMOKE_TIMEOUT" qemu-system-x86_64 \
    -nographic -no-reboot -m 1024M -smp 2 \
    $([ -e /dev/kvm ] && echo "-enable-kvm" || true) \
    -kernel "$kernel" -initrd "$initrd" \
    -append "console=ttyS0 quiet loglevel=0 panic=1" \
    -serial stdio 2>&1 | tee "$log" \
    | grep --color=never -E "(containerd|nydus|stargz|bonnie|panic|ERROR)" || true
  grep -q "Kernel panic" "$log" && die "Kernel panic detected - see $log"
  ok "Smoke log -> $log"
}

sign() {
  [[ -z "$PUSH_REGISTRY" ]] && { warn "PUSH_REGISTRY not set - skipping sign"; return; }
  step "Signing ${PUSH_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
  command -v cosign &>/dev/null || die "cosign not installed"
  cosign sign --yes "${PUSH_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
  ok "Signed"
}

push() {
  [[ -z "$PUSH_REGISTRY" ]] && { warn "PUSH_REGISTRY not set - skipping push"; return; }
  step "Pushing ${PUSH_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
  "$LINUXKIT_BIN" push --registry "$PUSH_REGISTRY" \
    --name "$IMAGE_NAME" --tag "$IMAGE_TAG" "$OUTPUT_DIR"
  ok "Pushed"
}

cmd="${1:-build}"
case "$cmd" in
  lint)   check_deps; lint ;;
  build)  check_deps; lint; build; sbom ;;
  smoke)  smoke ;;
  sign)   sign ;;
  push)   push ;;
  all)    check_deps; lint; build; sbom; smoke; push; sign ;;
  *)      echo "Usage: $0 [lint|build|smoke|sign|push|all]"; exit 1 ;;
esac
