#!/usr/bin/env bash
# How long can up's terminal be suspended (Ctrl-Z), or the whole machine sleep, before the environment breaks?
# Run from examples/greeting.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()
n=0

ensure_up() {
  if [ -n "$PID" ] && kill -0 $PID 2>/dev/null && kc .devenv/a/mgmt.kubeconfig get nodes --no-headers | grep -q Ready; then return 0; fi
  [ -n "$PID" ] && { kill -9 -- -$PID 2>/dev/null; sleep 2; }
  n=$((n + 1)); start_up "$L/up$n.log" up --name a
  wait_log "$L/up$n.log" " is up\." 1200 || { tail_state a; exit 1; }
}

healthy() { # healthy LABEL reports whether the environment still works, and for how long it fails
  local t=$SECONDS ok=no
  for i in $(seq 1 12); do
    if kc .devenv/a/mgmt.kubeconfig get nodes --no-headers | grep -q Ready && kc .devenv/a/workload.kubeconfig get nodes --no-headers | grep -q Ready; then ok=yes; break; fi
    sleep 10
  done
  obs "$1: healthy=$ok after $((SECONDS - t))s; up alive=$(kill -0 $PID 2>/dev/null && echo yes || echo no); status: $(/tmp/devenv status | grep '^a ' | awk '{print $2}'); $(sessions)"
  [ $ok = no ] && obs "kubectl: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1)"
  summary+=("$1: healthy=$ok")
}

for s in 10 30 60; do
  step "Ctrl-Z for ${s}s"
  ensure_up
  kill -STOP -- -$PID; sleep $s; kill -CONT -- -$PID; sleep 2
  healthy "Ctrl-Z ${s}s"
done

for s in 30 120; do
  step "machine sleep for ${s}s: pause the engine and stop up"
  ensure_up
  e=$(engine)
  docker pause "$e" >/dev/null; kill -STOP -- -$PID; sleep $s; docker unpause "$e" >/dev/null; kill -CONT -- -$PID; sleep 2
  healthy "sleep ${s}s"
done

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
