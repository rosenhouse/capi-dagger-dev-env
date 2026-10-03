#!/usr/bin/env bash
# Shared helpers for explore/ci scripts. Source it.
T=${RUNNER_TEMP:-/tmp}
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
SUMMARY=$T/summary.txt
: >"$SUMMARY"
obs() { echo "OBS: $*"; echo "$*" >>"$SUMMARY"; }
iface=$(ip route show default | awk '{print $5; exit}')
rx() { cat "/sys/class/net/$iface/statistics/rx_bytes"; }
engine() { docker ps --filter name=dagger-engine --format '{{.Names}}' | head -1; }
engine_mb() { local e; e=$(engine); [ -n "$e" ] && docker exec "$e" du -sm /var/lib/dagger 2>/dev/null | cut -f1; }
pause() { "$REPO/explore/lib/pause.sh" "$1"; }

# run LABEL CMD... runs CMD with stdout/stderr in $T/LABEL.{out,err} and reports exit, time and download.
run() {
  local label=$1; shift
  local rx0 t0 rc
  rx0=$(rx); t0=$SECONDS
  "$@" >"$T/$label.out" 2>"$T/$label.err"
  rc=$?
  obs "$label: exit=$rc time=$((SECONDS - t0))s rx=$((($(rx) - rx0) / 1048576))MB engine_disk=$(engine_mb)MB"
  echo "--- $label stdout"; tail -15 "$T/$label.out"
  echo "--- $label stderr"; tail -30 "$T/$label.err"
  return $rc
}

# wait_for FILE PATTERN SECONDS waits until FILE contains PATTERN.
wait_for() {
  local end=$((SECONDS + $3))
  while [ $SECONDS -lt $end ]; do
    grep -q "$2" "$1" 2>/dev/null && return 0
    sleep 2
  done
  return 1
}

finish() {
  echo "===================== SUMMARY ====================="
  cat "$SUMMARY"
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] && { echo '```'; cat "$SUMMARY"; echo '```'; } >>"$GITHUB_STEP_SUMMARY"
}
