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

# start_squid [DENIED_DOMAIN...] starts squid on the host at :3128 and rejects direct egress from the Docker bridge.
PX=http://172.17.0.1:3128
start_squid() {
  mkdir -p "$T/squid/log" && chmod 777 "$T/squid/log"
  {
    echo "http_port 3128"
    echo "acl localnet src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8"
    [ $# -gt 0 ] && echo "acl denied dstdomain $*" && echo "http_access deny denied"
    echo "http_access allow localnet"
    echo "http_access deny all"
    echo "cache deny all"
    echo "access_log stdio:/var/log/squid/access.log"
  } >"$T/squid/squid.conf"
  docker run -d --name squid --network host -v "$T/squid/squid.conf:/etc/squid/squid.conf:ro" -v "$T/squid/log:/var/log/squid" ubuntu/squid:latest >/dev/null
  for _ in $(seq 30); do curl -s -o /dev/null -x http://127.0.0.1:3128 https://registry-1.docker.io/v2/ && break; sleep 2; done
  sudo iptables -I DOCKER-USER -i docker0 ! -o docker0 -p tcp -m multiport --dports 80,443 -j REJECT --reject-with tcp-reset
}
squid_summary() {
  echo "--- squid: requests by result, method, host"
  sudo awk '{u=$7; sub(/^https?:\/\//,"",u); sub(/[:\/].*/,"",u); print $4, $6, u}' "$T/squid/log/access.log" | sort | uniq -c | sort -rn | head -40
  obs "squid: hosts: $(sudo awk '{u=$7; sub(/^https?:\/\//,"",u); sub(/[:\/].*/,"",u); print u}' "$T/squid/log/access.log" | sort -u | tr '\n' ' ')"
  obs "squid: bytes by host: $(sudo awk '{u=$7; sub(/^https?:\/\//,"",u); sub(/[:\/].*/,"",u); b[u]+=$5} END {for (h in b) printf "%s=%dMB ", h, b[h]/1048576}' "$T/squid/log/access.log")"
  sudo truncate -s0 "$T/squid/log/access.log"
}

finish() {
  echo "===================== SUMMARY ====================="
  cat "$SUMMARY"
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] && { echo '```'; cat "$SUMMARY"; echo '```'; } >>"$GITHUB_STEP_SUMMARY"
}
