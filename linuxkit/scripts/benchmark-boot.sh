#!/usr/bin/env bash
# =============================================================================
# scripts/benchmark-boot.sh
#
# Boots bonnie-cicd in QEMU, timestamps every service milestone, and prints
# a colour-coded boot waterfall report with pass/warn/fail per target.
#
# Usage:
#   bash scripts/benchmark-boot.sh [output-dir]
#
# Outputs:
#   boot-report-<ts>.txt    human-readable waterfall
#   boot-report-<ts>.json   machine-readable for CI dashboards
#   boot-raw-<ts>.log       full serial output with timestamps
#
# Requirements: qemu-system-x86_64, python3
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[1;33m'
CYN='\033[0;36m'; BLD='\033[1m'; RST='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_DIR="${1:-${ROOT_DIR}/output}"
IMAGE_NAME="${IMAGE_NAME:-bonnie-cicd}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-30}"
MEM="${MEM:-1024M}"
CPUS="${CPUS:-4}"

TS=$(date +%Y%m%dT%H%M%S)
LOG_RAW="${OUTPUT_DIR}/boot-raw-${TS}.log"
REPORT_TXT="${OUTPUT_DIR}/boot-report-${TS}.txt"
REPORT_JSON="${OUTPUT_DIR}/boot-report-${TS}.json"

KERNEL="${OUTPUT_DIR}/${IMAGE_NAME}-kernel"
INITRD="${OUTPUT_DIR}/${IMAGE_NAME}-initrd.img"

[[ -f "$KERNEL" ]] || {
  echo -e "${RED}FAIL${RST}  kernel not found: $KERNEL"
  echo    "  Rebuild with: FORMAT=kernel+initrd bash build.sh build"
  exit 1
}
[[ -f "$INITRD" ]] || { echo -e "${RED}FAIL${RST}  initrd not found: $INITRD"; exit 1; }

mkdir -p "$OUTPUT_DIR"

# Milestone format: "label"  "grep pattern"  target_ms
MILESTONES=(
  "Kernel decompressed"      "Decompressing Linux"                  200
  "initrd mounted"           "Run /init"                            400
  "rngd ready"               "rngd.*started"                        500
  "cgroups v2 mounted"       "cgroup2.*mounted"                     600
  "sysctl applied"           "sysctl.*done"                         700
  "DHCP lease"               "dhcpcd.*lease"                       1200
  "containerd ready"         "containerd.*serving"                 2000
  "nydus-snapshotter ready"  "nydus.*snapshotter.*serving"         2500
  "stargz-snapshotter ready" "stargz.*serving"                     3000
  "buildkitd ready"          "buildkitd.*started"                  3500
  "bonnie ready"             "bonnie.*accepting"                   4000
  "node-exporter ready"      "node_exporter.*Listening"            4500
  "SYSTEM READY"             "bonnie-cicd.*immutable"              5000
)

echo -e "\n${BLD}${CYN}>> Booting ${IMAGE_NAME} in QEMU (timeout=${BOOT_TIMEOUT}s)${RST}"

KVM_FLAG=""
[[ -e /dev/kvm ]] && KVM_FLAG="-enable-kvm" && echo -e "  ${GRN}KVM acceleration enabled${RST}"

START_EPOCH=$(date +%s%3N)

timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
  -nographic -no-reboot \
  -m "$MEM" -smp "$CPUS" \
  ${KVM_FLAG} \
  -kernel "$KERNEL" -initrd "$INITRD" \
  -append "console=ttyS0 quiet loglevel=0 panic=1 systemd.unified_cgroup_hierarchy=1 cgroup_no_v1=all transparent_hugepage=madvise vsyscall=none debugfs=off mitigations=auto apparmor=1 security=apparmor lsm=lockdown,yama,apparmor,bpf,landlock lockdown=integrity" \
  -serial stdio 2>&1 | while IFS= read -r line; do
    MS=$(( $(date +%s%3N) - START_EPOCH ))
    printf "%07d %s\n" "$MS" "$line"
  done | tee "$LOG_RAW" || true

END_EPOCH=$(date +%s%3N)
TOTAL_MS=$(( END_EPOCH - START_EPOCH ))

grep -q "Kernel panic" "$LOG_RAW" && {
  echo -e "\n${RED}FAIL  KERNEL PANIC detected — see $LOG_RAW${RST}"
  exit 1
}

echo -e "\n${BLD}${CYN}>> Boot Waterfall${RST}"
printf "\n  ${BLD}%-38s  %8s  %8s  %-8s${RST}\n" "Milestone" "Time(ms)" "Target" "Status"
printf "  %s\n" "$(printf '%.0s-' {1..72})"

N=${#MILESTONES[@]}
WORST_MISS=0
JSON="["
declare -A TIMES

for (( i=0; i<N; i+=3 )); do
  LABEL="${MILESTONES[$i]}"
  PAT="${MILESTONES[$((i+1))]}"
  TARGET="${MILESTONES[$((i+2))]}"

  MATCH=$(grep -m1 -i "$PAT" "$LOG_RAW" 2>/dev/null || true)
  if [[ -n "$MATCH" ]]; then
    MS=$(echo "$MATCH" | awk '{print $1}')
    DELTA=$(( MS - TARGET ))
    if   (( DELTA <= 0   )); then STATUS="${GRN}FAST${RST}"
    elif (( DELTA <= 500 )); then STATUS="${YLW}OK${RST}"
    else STATUS="${RED}SLOW${RST}"; (( DELTA > WORST_MISS )) && WORST_MISS=$DELTA
    fi
    printf "  %-38s  %8s  %8s  " "$LABEL" "${MS}ms" "${TARGET}ms"
    echo -e "$STATUS"
    TIMES["$LABEL"]=$MS
    JSON+="{\"milestone\":\"$LABEL\",\"ms\":$MS,\"target_ms\":$TARGET},"
  else
    printf "  %-38s  %8s  %8s  -\n" "$LABEL" "N/A" "${TARGET}ms"
    JSON+="{\"milestone\":\"$LABEL\",\"ms\":null,\"target_ms\":$TARGET},"
  fi
done

printf "  %s\n" "$(printf '%.0s-' {1..72})"
printf "  ${BLD}%-38s  %8s${RST}\n" "TOTAL" "${TOTAL_MS}ms"

if   (( WORST_MISS == 0   )); then VERDICT="PASS"; echo -e "\n  ${GRN}${BLD}All milestones within target${RST}"
elif (( WORST_MISS <= 1000)); then VERDICT="WARN"; echo -e "\n  ${YLW}Some milestones over target (+${WORST_MISS}ms worst miss)${RST}"
else                                VERDICT="FAIL"; echo -e "\n  ${RED}Boot degraded — worst miss: +${WORST_MISS}ms${RST}"
fi

echo -e "  Total: ${BLD}${TOTAL_MS}ms${RST}  (target <5000ms)"

# Write reports
{
  echo "bonnie-cicd Boot Benchmark -- ${TS}"
  echo "Total: ${TOTAL_MS}ms | Target: 5000ms | Verdict: ${VERDICT}"
  echo ""
  for (( i=0; i<N; i+=3 )); do
    L="${MILESTONES[$i]}"; T="${MILESTONES[$((i+2))]}"
    printf "  %-38s  %s / %sms\n" "$L" "${TIMES[$L]:-N/A}ms" "$T"
  done
} > "$REPORT_TXT"

JSON="${JSON%,}]"
python3 -c "
import json, sys
print(json.dumps({'timestamp':'${TS}','total_ms':${TOTAL_MS},
  'target_ms':5000,'verdict':'${VERDICT}',
  'kvm':$([[ -n "$KVM_FLAG" ]] && echo true || echo false),
  'milestones':${JSON}}, indent=2))
" > "$REPORT_JSON"

echo -e "\n  ${CYN}Reports:${RST}"
echo    "    $REPORT_TXT"
echo    "    $REPORT_JSON"
echo    "    $LOG_RAW"

[[ "$VERDICT" == "FAIL" ]] && exit 1 || exit 0
