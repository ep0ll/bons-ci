#!/usr/bin/env bash
# =============================================================================
# scripts/nydus-cache-gc.sh
#
# Garbage-collects the nydus blob cache by evicting the least-recently-used
# blob chunks until the cache is within the target size.
#
# Designed to run as a cron job inside the nydus-snapshotter service cgroup,
# or as a one-shot call from the host.
#
# Strategy:
#   1. Measure current cache size.
#   2. If under CACHE_MAX_GIB, do nothing.
#   3. Sort blobs by atime (oldest first).
#   4. Delete oldest blobs until cache <= CACHE_TARGET_GIB.
#   5. Log a structured JSON summary.
#
# Usage:
#   bash scripts/nydus-cache-gc.sh
#   bash scripts/nydus-cache-gc.sh --dry-run
#
# Env:
#   NYDUS_CACHE_DIR      blob cache root  (default: /var/lib/nydus/cache)
#   CACHE_MAX_GIB        start GC above this size in GiB  (default: 18)
#   CACHE_TARGET_GIB     target size after GC in GiB      (default: 14)
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

CYN='\033[0;36m'; YLW='\033[1;33m'; GRN='\033[0;32m'
RED='\033[0;31m'; BLD='\033[1m'; RST='\033[0m'

DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

CACHE_DIR="${NYDUS_CACHE_DIR:-/var/lib/nydus/cache}"
CACHE_MAX="${CACHE_MAX_GIB:-18}"
CACHE_TARGET="${CACHE_TARGET_GIB:-14}"
LOG_FILE="/var/log/nydus-cache-gc.log"

TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)

log() {
  local level="$1"; shift
  echo -e "${CYN}[nydus-gc]${RST} ${level}: $*"
}

[[ -d "$CACHE_DIR" ]] || { log "INFO" "Cache dir not found: $CACHE_DIR — nothing to do"; exit 0; }

# Current cache size in GiB (integer)
current_gib() {
  du -sg "$CACHE_DIR" 2>/dev/null | awk '{print $1}' || echo 0
}

BEFORE_GIB=$(current_gib)
log "INFO" "Cache before: ${BEFORE_GIB} GiB (max: ${CACHE_MAX} GiB, target: ${CACHE_TARGET} GiB)"

if (( BEFORE_GIB <= CACHE_MAX )); then
  log "INFO" "Cache within limit — no GC needed"
  printf '{"timestamp":"%s","action":"skip","before_gib":%d,"max_gib":%d}\n' \
    "$TS" "$BEFORE_GIB" "$CACHE_MAX" >> "$LOG_FILE" 2>/dev/null || true
  exit 0
fi

log "INFO" "Cache exceeds limit — starting GC"
[[ "$DRY_RUN" == "1" ]] && log "INFO" "(DRY RUN — no files will be deleted)"

# Find blob files sorted by access time (oldest first)
# nydus stores blobs as flat files named by their sha256 hash
DELETED_COUNT=0
DELETED_BYTES=0

while IFS= read -r -d '' blobfile; do
  CURRENT=$(current_gib)
  (( CURRENT <= CACHE_TARGET )) && break

  SIZE=$(stat -c%s "$blobfile" 2>/dev/null || echo 0)
  if [[ "$DRY_RUN" == "0" ]]; then
    rm -f "$blobfile"
    (( DELETED_COUNT++ ))
    (( DELETED_BYTES += SIZE ))
    log "DEBUG" "Evicted: $(basename "$blobfile") ($(( SIZE / 1048576 )) MiB)"
  else
    log "DEBUG" "Would evict: $(basename "$blobfile") ($(( SIZE / 1048576 )) MiB)"
    (( DELETED_COUNT++ ))
    (( DELETED_BYTES += SIZE ))
  fi
done < <(find "$CACHE_DIR" -type f -name '*.blob' -printf '%A@\t%p\0' 2>/dev/null \
         | sort -z -n \
         | cut -z -f2-)

AFTER_GIB=$(current_gib)
FREED_MIB=$(( DELETED_BYTES / 1048576 ))

log "INFO" "GC complete: evicted ${DELETED_COUNT} blob(s), freed ~${FREED_MIB} MiB"
log "INFO" "Cache after: ${AFTER_GIB} GiB"

# Structured log entry
printf '{"timestamp":"%s","dry_run":%s,"before_gib":%d,"after_gib":%d,"freed_mib":%d,"evicted_blobs":%d}\n' \
  "$TS" \
  "$([[ "$DRY_RUN" == "1" ]] && echo true || echo false)" \
  "$BEFORE_GIB" "$AFTER_GIB" "$FREED_MIB" "$DELETED_COUNT" \
  >> "$LOG_FILE" 2>/dev/null || true

# Emit Prometheus-compatible metric for scraping
PROM_FILE="/run/bonnie/nydus_gc.prom"
if [[ -d "$(dirname "$PROM_FILE")" ]]; then
  cat > "$PROM_FILE" << PROMEOF
# HELP nydus_cache_gc_freed_mib MiB freed in last GC run
# TYPE nydus_cache_gc_freed_mib gauge
nydus_cache_gc_freed_mib ${FREED_MIB}
# HELP nydus_cache_gc_evicted_blobs Blobs evicted in last GC run
# TYPE nydus_cache_gc_evicted_blobs gauge
nydus_cache_gc_evicted_blobs ${DELETED_COUNT}
# HELP nydus_cache_size_gib Current blob cache size in GiB
# TYPE nydus_cache_size_gib gauge
nydus_cache_size_gib ${AFTER_GIB}
PROMEOF
fi
