#!/usr/bin/env bash
# =============================================================================
# scripts/health-check.sh
#
# Runtime health check for bonnie-cicd.
# Suitable as a Kubernetes liveness/readiness probe, a Prometheus blackbox
# exporter target, or a simple cron-driven watchdog.
#
# Checks:
#   - All critical unix sockets exist and respond
#   - containerd can list namespaces
#   - nydus-snapshotter metrics endpoint is up
#   - stargz-snapshotter metrics endpoint is up
#   - bonnie gRPC endpoint is reachable
#   - node-exporter metrics endpoint is up
#   - No OOM kills since last check
#   - Disk pressure on tmpfs mounts
#   - CPU steal time within tolerance
#
# Exit codes:
#   0 = healthy
#   1 = degraded (some checks failed, system still functional)
#   2 = critical (bonnie or containerd unreachable)
#
# Usage:
#   bash scripts/health-check.sh [--json] [--quiet]
#
# Env:
#   HEALTH_DISK_WARN_PCT   warn threshold for tmpfs usage  (default: 80)
#   HEALTH_DISK_CRIT_PCT   critical threshold              (default: 95)
#   HEALTH_STEAL_WARN_PCT  CPU steal warn threshold        (default: 10)
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

GRN='\033[0;32m'; RED='\033[0;31m'; YLW='\033[1;33m'
BLD='\033[1m'; CYN='\033[0;36m'; RST='\033[0m'

JSON_MODE=0
QUIET_MODE=0
for arg in "$@"; do
  case "$arg" in
    --json)  JSON_MODE=1 ;;
    --quiet) QUIET_MODE=1 ;;
  esac
done

DISK_WARN="${HEALTH_DISK_WARN_PCT:-80}"
DISK_CRIT="${HEALTH_DISK_CRIT_PCT:-95}"
STEAL_WARN="${HEALTH_STEAL_WARN_PCT:-10}"

TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
DEGRADED=0
CRITICAL=0

declare -a RESULTS=()

record() {
  local name="$1" status="$2" msg="$3"
  RESULTS+=("{\"check\":\"${name}\",\"status\":\"${status}\",\"msg\":\"${msg}\"}")
  if [[ "$QUIET_MODE" == "0" ]]; then
    case "$status" in
      ok)       echo -e "  ${GRN}OK${RST}       ${name}: ${msg}" ;;
      degraded) echo -e "  ${YLW}DEGRADED${RST} ${name}: ${msg}" ;;
      critical) echo -e "  ${RED}CRITICAL${RST} ${name}: ${msg}" ;;
    esac
  fi
}

# ── Socket checks ─────────────────────────────────────────────────────────────
check_socket() {
  local name="$1" path="$2" severity="${3:-critical}"
  if [[ -S "$path" ]]; then
    record "$name" "ok" "socket exists: $path"
    return 0
  else
    record "$name" "$severity" "socket missing: $path"
    [[ "$severity" == "critical" ]] && (( CRITICAL++ )) || (( DEGRADED++ ))
    return 1
  fi
}

check_socket "containerd.socket"         "/run/containerd/containerd.sock"         "critical"
check_socket "nydus.socket"              "/run/containerd-nydus/containerd-nydus-grpc.sock" "degraded"
check_socket "stargz.socket"            "/run/containerd-stargz-grpc/containerd-stargz-grpc.sock" "degraded"
check_socket "buildkitd.socket"         "/run/buildkit/buildkitd.sock"             "degraded"
check_socket "bonnie.socket"            "/run/bonnie/bonnie.sock"                  "critical"

# ── containerd namespace list ─────────────────────────────────────────────────
if command -v ctr &>/dev/null && [[ -S /run/containerd/containerd.sock ]]; then
  if ctr --timeout 3s namespaces list &>/dev/null; then
    record "containerd.api" "ok" "namespace list succeeded"
  else
    record "containerd.api" "critical" "ctr namespaces list failed"
    (( CRITICAL++ ))
  fi
else
  record "containerd.api" "degraded" "ctr not available or socket missing"
  (( DEGRADED++ ))
fi

# ── HTTP metrics endpoints ────────────────────────────────────────────────────
check_http() {
  local name="$1" url="$2" severity="${3:-degraded}"
  local http_code
  http_code=$(curl -sf --max-time 3 -o /dev/null -w "%{http_code}" "$url" 2>/dev/null || echo "000")
  if [[ "$http_code" == "200" ]]; then
    record "$name" "ok" "HTTP 200 from $url"
  else
    record "$name" "$severity" "HTTP ${http_code} from $url"
    [[ "$severity" == "critical" ]] && (( CRITICAL++ )) || (( DEGRADED++ ))
  fi
}

check_http "containerd.metrics"   "http://127.0.0.1:1338/metrics" "degraded"
check_http "nydus.metrics"        "http://127.0.0.1:9090/metrics"  "degraded"
check_http "bonnie.metrics"       "http://127.0.0.1:9091/metrics"  "critical"
check_http "stargz.metrics"       "http://127.0.0.1:9092/metrics"  "degraded"
check_http "node-exporter"        "http://127.0.0.1:9100/metrics"  "degraded"

# ── OOM kills since last boot ─────────────────────────────────────────────────
if [[ -f /proc/vmstat ]]; then
  OOM=$(grep -E "^oom_kill " /proc/vmstat | awk '{print $2}' || echo 0)
  if (( OOM == 0 )); then
    record "oom_kills" "ok" "no OOM kills recorded"
  elif (( OOM < 5 )); then
    record "oom_kills" "degraded" "${OOM} OOM kill(s) since boot"
    (( DEGRADED++ ))
  else
    record "oom_kills" "critical" "${OOM} OOM kills since boot — check memory pressure"
    (( CRITICAL++ ))
  fi
fi

# ── tmpfs disk pressure ───────────────────────────────────────────────────────
for mount in /run /tmp /var/lib/nydus /cache/bonnie; do
  [[ -d "$mount" ]] || continue
  PCT=$(df -P "$mount" 2>/dev/null | awk 'NR==2 {gsub(/%/,""); print $5}' || echo 0)
  if   (( PCT >= DISK_CRIT )); then
    record "disk.${mount//\//_}" "critical" "${mount} at ${PCT}% (>=${DISK_CRIT}%)"
    (( CRITICAL++ ))
  elif (( PCT >= DISK_WARN )); then
    record "disk.${mount//\//_}" "degraded" "${mount} at ${PCT}% (>=${DISK_WARN}%)"
    (( DEGRADED++ ))
  else
    record "disk.${mount//\//_}" "ok" "${mount} at ${PCT}%"
  fi
done

# ── CPU steal time ────────────────────────────────────────────────────────────
if command -v mpstat &>/dev/null; then
  STEAL=$(mpstat 1 1 2>/dev/null | awk '/Average/ {printf "%.0f", $9}' || echo 0)
  if (( STEAL >= STEAL_WARN )); then
    record "cpu.steal" "degraded" "CPU steal ${STEAL}% (>=${STEAL_WARN}%) — noisy neighbour"
    (( DEGRADED++ ))
  else
    record "cpu.steal" "ok" "CPU steal ${STEAL}%"
  fi
fi

# ── Cgroup v2 sanity ──────────────────────────────────────────────────────────
if mount | grep -q "cgroup2"; then
  record "cgroups.v2" "ok" "cgroup v2 mounted"
else
  record "cgroups.v2" "critical" "cgroup v2 not mounted"
  (( CRITICAL++ ))
fi

# ── Nydus cache GC headroom ──────────────────────────────────────────────────
NYDUS_CACHE="/var/lib/nydus/cache"
if [[ -d "$NYDUS_CACHE" ]]; then
  CACHE_GB=$(du -sg "$NYDUS_CACHE" 2>/dev/null | awk '{print $1}' || echo 0)
  if (( CACHE_GB > 18 )); then
    record "nydus.cache" "degraded" "Nydus blob cache ${CACHE_GB} GiB (limit 20 GiB) — GC soon"
    (( DEGRADED++ ))
  else
    record "nydus.cache" "ok" "Nydus blob cache ${CACHE_GB} GiB"
  fi
fi

# ── Output ────────────────────────────────────────────────────────────────────
if [[ "$CRITICAL" -gt 0 ]]; then
  OVERALL="critical"
elif [[ "$DEGRADED" -gt 0 ]]; then
  OVERALL="degraded"
else
  OVERALL="ok"
fi

if [[ "$JSON_MODE" == "1" ]]; then
  printf '{'
  printf '"timestamp":"%s",' "$TS"
  printf '"overall":"%s",'  "$OVERALL"
  printf '"critical":%d,'   "$CRITICAL"
  printf '"degraded":%d,'   "$DEGRADED"
  printf '"checks":['
  printf '%s,' "${RESULTS[@]}"
  printf ']}'  | sed 's/,]/]/'
  printf '\n'
fi

if [[ "$QUIET_MODE" == "0" ]]; then
  echo ""
  if   [[ "$OVERALL" == "ok"       ]]; then echo -e "  ${GRN}${BLD}HEALTHY${RST}"
  elif [[ "$OVERALL" == "degraded" ]]; then echo -e "  ${YLW}${BLD}DEGRADED${RST} — ${DEGRADED} issue(s)"
  else                                       echo -e "  ${RED}${BLD}CRITICAL${RST} — ${CRITICAL} critical, ${DEGRADED} degraded"
  fi
fi

case "$OVERALL" in
  ok)       exit 0 ;;
  degraded) exit 1 ;;
  critical) exit 2 ;;
esac
