#!/usr/bin/env bash
# Follow-ups: down --purge without --name, down and Ctrl-C after the engine vanished.
# Run from examples/greeting.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()

step "F1 down --purge without --name, with a running and a stopped environment"
start_up "$L/old.log" up --name old
wait_log "$L/old.log" " is up\." 1200 || { tail_state old; exit 1; }
kill -INT -- -$PID; wait_exit $PID 120
start_up "$L/a.log" up --name a; A=$PID
wait_log "$L/a.log" " is up\." 900 || { tail_state a; exit 1; }
obs "status:"; status
obs "down --purge (no --name):"; /tmp/devenv down --purge 2>&1 | sed 's/^/    /'
wait_exit $A 60
obs "status after:"; status
summary+=("down --purge without --name: left $(ls .devenv | tr '\n' ' ')")

step "F2 the engine vanishes under a running environment, then down and Ctrl-C"
start_up "$L/a2.log" up --name a; A=$PID
wait_log "$L/a2.log" " is up\." 900 || { tail_state a; exit 1; }
curl -fsSL https://dl.dagger.io/dagger/install.sh | DAGGER_VERSION=0.21.9 BIN_DIR=/tmp/other sh >/dev/null 2>&1
DAGGER_NO_NAG=1 timeout 600 /tmp/other/dagger -s core version >/dev/null 2>&1
obs "engines: $(docker ps -a --filter name=dagger-engine --format '{{.Names}}' | tr '\n' ' ')"
obs "status: $(/tmp/devenv status | grep '^a ' | tr -s ' ')"
t=$SECONDS; out=$(timeout 420 /tmp/devenv down --name a 2>&1); rc=$?
obs "down after the engine vanished: rc=$rc after $((SECONDS - t))s: $(echo "$out" | tr '\n' ' ')"
summary+=("down after engine vanished: rc=$rc after $((SECONDS - t))s")
if kill -0 $A 2>/dev/null; then
  kill -INT -- -$A; t=$SECONDS; wait_exit $A 400; summary+=("Ctrl-C after engine vanished: exit after $((SECONDS - t))s")
fi
tail -5 "$L/a2.log"

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
