#!/usr/bin/env bash
# A disk that fills after the build, while a new environment's Docker daemon and clusters start.
# Run from examples/greeting.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()

step "F3 the disk fills after the build, while a new environment's Docker daemon pulls"
start_up "$L/w.log" up --name warm
wait_log "$L/w.log" " is up\." 1200 || tail_state warm
kill -INT -- -$PID; wait_exit $PID 300
avail=$(df --output=avail -B1G / | tail -1 | tr -d ' ')
sudo fallocate -l $((avail - ${FREE_GB:-3}))G /fill && obs "filled the disk; $(df -h / | tail -1)"
start_up "$L/full.log" up --name full
t=$SECONDS
if wait_log "$L/full.log" " is up\." 900; then
  summary+=("up --name full with ${FREE_GB:-3}G free succeeded"); kill -INT -- -$PID; wait_exit $PID 120
else
  if kill -0 $PID 2>/dev/null; then
    obs "still running at $(grep -o '\] .*' "$L/full.log" | tail -1); Ctrl-C"; kill -INT -- -$PID; wait_exit $PID 300 || kill -9 -- -$PID
  fi
  obs "output tail:"; tail -25 "$L/full.log"
  summary+=("up --name full with ${FREE_GB:-3}G free failed after $((SECONDS - t))s: $(grep -m1 '^Error' "$L/full.log" | cut -c1-200)")
fi
sudo rm -f /fill
obs "engine: $(docker ps -a --filter name=dagger-engine --format '{{.Names}} {{.Status}}')"

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
