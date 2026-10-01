#!/usr/bin/env bash
# E2 restore: restores each checkpoint under DIR/ckpt-*/ on this host.
# Appends one tab-separated row per checkpoint to ROWS; keeps failed consoles in DIAG.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
ckpts=$1 rows=$2 diag=$3
mine=$(host_contract) model=$(cpu_model)
mkdir -p "$diag"

summary "Restore host: $model, $mine, $(host_info)"
summary ""
summary "| captured on | capture contract | predicted | result | create --from | start | served | guest clock |"
summary "|---|---|---|---|---|---|---|---|"
for d in "$ckpts"/ckpt-*; do
  theirs=$(cat "$d/contract.txt")
  predicted=miss
  [ "$theirs" = "$mine" ] && predicted=hit
  create_s="" start_s="" served="" clock_note=""
  t=$EPOCHREALTIME
  if out=$(smolvm machine create --name r --from "$d/tiny.checkpoint" 2>&1); then
    create_s=$(since "$t") t=$EPOCHREALTIME
    if out=$(smolvm machine start --name r 2>&1); then
      start_s=$(since "$t")
      wait_serving 18080 || true
      served=$(probe 18080)
      read -r h g u < <(clock r)
      read -r h0 u0 <"$d/clock.txt"
      clock_note="uptime +$(diff_s "$u0" "$u") over host +$(diff_s "$h0" "$h"); realtime offset $(diff_s "$h" "$g")"
      result=ok
      [ -n "$served" ] || result="not serving"
    else
      start_s=$(since "$t") result="start failed: $(tail -1 <<<"$out")"
    fi
  else
    create_s=$(since "$t") result="refused: $(tail -1 <<<"$out")"
  fi
  if [ "$result" != ok ]; then
    cp "$(smolvm machine data-dir --name r)/agent-console.log" "$diag/$(basename "$d")-console.log" 2>/dev/null || true
  fi
  smolvm machine delete --name r -f >/dev/null 2>&1 || true
  summary "| $(cat "$d/model.txt") | ${theirs:0:24} | $predicted | $result | ${create_s:-?} s | ${start_s:-?} s | $served | $clock_note |"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$(cat "$d/model.txt")" "$theirs" "$model" "$mine" \
    "$predicted" "$result" "$create_s" "$start_s" "$served" "$clock_note" >>"$rows"
done
