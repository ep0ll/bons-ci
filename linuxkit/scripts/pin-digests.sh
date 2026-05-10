#!/usr/bin/env bash
# =============================================================================
# scripts/pin-digests.sh
#
# Resolves every image:tag reference in bonnie.yml to image:tag@sha256:...
# and rewrites the file in-place.
#
# Run before each release for fully reproducible, supply-chain-safe builds.
# Commit the resulting bonnie.yml to version control.
#
# Usage:
#   bash scripts/pin-digests.sh               # operates on bonnie.yml
#   bash scripts/pin-digests.sh other.yml     # arbitrary file
#
# Requirements: docker (or crane), python3 + pyyaml
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[1;33m'
CYN='\033[0;36m'; RST='\033[0m'

log()  { echo -e "${CYN}[pin-digests]${RST} $*"; }
ok()   { echo -e "  ${GRN}OK${RST}  $*"; }
warn() { echo -e "  ${YLW}WARN${RST}  $*"; }
die()  { echo -e "  ${RED}FAIL${RST}  $*" >&2; exit 1; }

TARGET="${1:-$(cd "$(dirname "$0")/.." && pwd)/bonnie.yml}"
[[ -f "$TARGET" ]] || die "File not found: $TARGET"

# Prefer crane (no daemon needed) but fall back to docker
if command -v crane &>/dev/null; then
  resolve() { crane digest "$1" 2>/dev/null || true; }
elif command -v docker &>/dev/null; then
  resolve() {
    docker pull "$1" -q &>/dev/null || true
    docker inspect --format '{{index .RepoDigests 0}}' "$1" 2>/dev/null \
      | awk -F@ '{print $2}' || true
  }
else
  die "Neither 'crane' nor 'docker' found. Install one and retry."
fi

log "Resolving image digests in: $TARGET"
BACKUP="${TARGET}.bak-$(date +%s)"
cp "$TARGET" "$BACKUP"
log "Backup: $BACKUP"

images=$(python3 - "$TARGET" << 'PYEOF'
import sys, yaml, pathlib, re
doc = yaml.safe_load(pathlib.Path(sys.argv[1]).read_text())
seen = set()
for section in ("init", "onboot", "services"):
    items = doc.get(section, [])
    if section == "init":
        items = [{"image": i} for i in items]
    for item in items:
        img = item.get("image", "")
        if img and img not in seen and "@sha256:" not in img:
            print(img)
        seen.add(img)
PYEOF
)

if [[ -z "$images" ]]; then
  ok "All images already pinned — nothing to do."
  rm -f "$BACKUP"
  exit 0
fi

CHANGED=0
while IFS= read -r img; do
  [[ -z "$img" ]] && continue
  log "Resolving $img ..."
  digest=$(resolve "$img" 2>/dev/null || true)
  if [[ -z "$digest" || "$digest" == "sha256:" ]]; then
    warn "Could not resolve $img — leaving unpinned"
    continue
  fi
  repo="${img%%:*}"
  tag="${img#*:}"
  [[ "$tag" == "$img" ]] && tag="latest"
  pinned="${repo}:${tag}@${digest}"
  # Escape slashes and dots for sed
  esc_img=$(printf '%s' "$img"    | sed 's/[[\.*^$()+?{|]/\\&/g; s|/|\\/|g')
  esc_pin=$(printf '%s' "$pinned" | sed 's/[[\.*^$()+?{|]/\\&/g; s|/|\\/|g')
  sed -i "s|image: ${esc_img}$|image: ${esc_pin}|g" "$TARGET"
  ok "$img  ->  $pinned"
  (( CHANGED++ )) || true
done <<< "$images"

log "Done — $CHANGED image(s) pinned."
log "Review: diff $BACKUP $TARGET"
