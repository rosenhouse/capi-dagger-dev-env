#!/usr/bin/env bash
# Which activity during a Ctrl-Z of up breaks the environment: a kubectl watch, a one-off kubectl, or a redeploy?
# Run from examples/greeting.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()
n=0

fresh_up() {
  [ -n "$PID" ] && { kill -9 -- -$PID 2>/dev/null; sleep 3; }
  n=$((n + 1)); start_up "$L/up$n.log" up --name a
  wait_log "$L/up$n.log" " is up\." 1200 || { tail_state a; exit 1; }
}

healthy() {
  local t=$SECONDS ok=no
  for i in $(seq 1 9); do
    if kc .devenv/a/mgmt.kubeconfig get nodes --no-headers | grep -q Ready && kc .devenv/a/workload.kubeconfig get nodes --no-headers | grep -q Ready; then ok=yes; break; fi
    sleep 10
  done
  obs "$1: healthy=$ok after $((SECONDS - t))s; up alive=$(kill -0 $PID 2>/dev/null && echo yes || echo no); status: $(/tmp/devenv status | grep '^a ' | awk '{print $2}'); $(sessions)"
  [ $ok = no ] && obs "kubectl: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1); redeploy: $(timeout 120 /tmp/devenv redeploy --name a 2>&1 | tail -1)"
  summary+=("$1: healthy=$ok")
}

step "Z1 Ctrl-Z for 60s while a kubectl watch is open, as k9s keeps one"
fresh_up
timeout 300 kubectl --kubeconfig .devenv/a/mgmt.kubeconfig get pods -A -w > "$L/watch.log" 2>&1 &
W=$!
sleep 10
kill -STOP -- -$PID; sleep 60; kill -CONT -- -$PID; sleep 2
healthy "Ctrl-Z 60s with a kubectl watch"
kill $W 2>/dev/null; obs "watch tail: $(tail -2 "$L/watch.log" | tr '\n' ' ')"

step "Z2 Ctrl-Z for 60s with one kubectl request during it"
[ "${summary[-1]}" = "Ctrl-Z 60s with a kubectl watch: healthy=yes" ] || fresh_up
kill -STOP -- -$PID; sleep 5
obs "kubectl during: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1)"
sleep 45; kill -CONT -- -$PID; sleep 2
healthy "Ctrl-Z 60s with one kubectl request"

step "Z3 Ctrl-Z for 60s with one redeploy request during it"
[ "${summary[-1]}" = "Ctrl-Z 60s with one kubectl request: healthy=yes" ] || fresh_up
kill -STOP -- -$PID; sleep 5
obs "redeploy during: $(timeout 20 /tmp/devenv redeploy --name a 2>&1 | tail -1)"
sleep 35; kill -CONT -- -$PID; sleep 2
healthy "Ctrl-Z 60s with one redeploy request"

step "Z4 Ctrl-Z for 90s with a kubectl request and a redeploy request during it, as in the first run"
[ "${summary[-1]}" = "Ctrl-Z 60s with one redeploy request: healthy=yes" ] || fresh_up
kill -STOP -- -$PID
obs "kubectl during: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1)"
obs "redeploy during: $(timeout 30 /tmp/devenv redeploy --name a 2>&1 | tail -1)"
sleep 50; kill -CONT -- -$PID; sleep 5
healthy "Ctrl-Z 90s with kubectl and redeploy"

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
