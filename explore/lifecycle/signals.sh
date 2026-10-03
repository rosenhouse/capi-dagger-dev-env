#!/usr/bin/env bash
# Signals and interruptions of `up`: Ctrl-Z, laptop sleep, kill -9, closing the terminal, Ctrl-C during bring-up.
# Run from examples/greeting.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()
E=$(pwd)/.devenv/a
sock() { echo ~/.cache/devenv/$(printf %s "$E" | sha256sum | cut -c1-16).sock; }

step "S1 cold up --name a, polling status while it comes up"
disk before
start_up "$L/up1.log" up --name a
t=$SECONDS
while kill -0 $PID 2>/dev/null && ! grep -q " is up\." "$L/up1.log"; do
  obs "status at $((SECONDS - t))s: $(/tmp/devenv status | grep '^a ' | tr -s ' ') | last stage: $(grep -o '\] .*' "$L/up1.log" | tail -1)"
  sleep 30
done
grep -q " is up\." "$L/up1.log" || { obs "up1 failed"; tail -30 "$L/up1.log"; tail_state a; exit 1; }
obs "cold up took $((SECONDS - t))s"; summary+=("cold up $((SECONDS - t))s")
cat "$L/up1.log" | grep -v '^\['
P1=$(ports a); obs "ports up1: $P1"
kc .devenv/a/mgmt.kubeconfig get nodes; kc .devenv/a/workload.kubeconfig get nodes
disk "a up"; obs "sessions: $(sessions)"

step "S2 Ctrl-Z: SIGSTOP up's process group for 120s while the engine runs"
kill -STOP -- -$PID
obs "status while stopped: $(/tmp/devenv status | grep '^a ' | tr -s ' ')"
obs "kubectl mgmt while stopped: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1)"
obs "kubeconfig cmd while stopped: $(timeout 20 /tmp/devenv kubeconfig --name a 2>&1 | head -c 80 | tr '\n' ' ') rc=$?"
t=$SECONDS; out=$(timeout 30 /tmp/devenv redeploy --name a 2>&1); obs "redeploy while stopped: rc=$? after $((SECONDS - t))s: $(echo "$out" | tail -2 | tr '\n' ' ')"
sleep 60
kill -CONT -- -$PID
sleep 5
obs "up alive after CONT: $(kill -0 $PID 2>/dev/null && echo yes || echo no)"
obs "kubectl mgmt after CONT: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1)"
obs "kubectl workload after CONT: $(kc .devenv/a/workload.kubeconfig get nodes | tail -1)"
t=$SECONDS; out=$(timeout 600 /tmp/devenv redeploy --name a 2>&1); rc=$?
obs "redeploy after CONT: rc=$rc after $((SECONDS - t))s: $(echo "$out" | tail -2 | tr '\n' ' ')"; summary+=("redeploy after Ctrl-Z rc=$rc")

step "S3 laptop sleep: pause the engine and stop up's process group for 150s"
eng=$(engine)
docker pause "$eng" >/dev/null; kill -STOP -- -$PID
sleep 150
docker unpause "$eng" >/dev/null; kill -CONT -- -$PID
t=$SECONDS
for i in $(seq 1 24); do
  if ! kill -0 $PID 2>/dev/null; then obs "up exited after wake"; tail -15 "$L/up1.log"; break; fi
  m=$(kc .devenv/a/mgmt.kubeconfig get nodes --no-headers | tail -1); w=$(kc .devenv/a/workload.kubeconfig get nodes --no-headers | tail -1)
  obs "after wake +$((SECONDS - t))s: mgmt: $m | workload: $w"
  echo "$m$w" | grep -q 'Ready.*Ready' && break
  sleep 10
done
t=$SECONDS; out=$(timeout 600 /tmp/devenv redeploy --name a 2>&1); rc=$?
obs "redeploy after wake: rc=$rc after $((SECONDS - t))s: $(echo "$out" | tail -2 | tr '\n' ' ')"; summary+=("redeploy after sleep rc=$rc")
tail -5 "$L/up1.log"

step "S4 kill -9 of up"
kill -0 $PID 2>/dev/null || { start_up "$L/up1b.log" up --name a; wait_log "$L/up1b.log" " is up\." 900; }
kill -9 $PID
t=$SECONDS
obs "status right after kill -9: $(/tmp/devenv status | grep '^a ' | tr -s ' ')"
obs "socket after kill -9: $(test -S "$(sock)" && echo present || echo gone); kubeconfigs on disk: $(ls .devenv/a/*.kubeconfig 2>/dev/null | wc -l)"
obs "kubectl through old kubeconfig: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1)"
for i in $(seq 1 12); do obs "+$((SECONDS - t))s after kill: $(sessions)"; echo "$(sessions)" | grep -q 'dockerd=0 apiservers=0' && break; sleep 10; done
disk "after kill -9 of a"

step "S5 up --name a again right after kill -9"
start_up "$L/up2.log" up --name a
t=$SECONDS
if wait_log "$L/up2.log" " is up\." 900; then
  P2=$(ports a); obs "re-up after kill -9 took $((SECONDS - t))s; ports $P1 -> $P2"; summary+=("re-up after kill -9 $((SECONDS - t))s; ports $P1 -> $P2")
else
  summary+=("re-up after kill -9 FAILED"); tail_state a
fi
grep '^\[' "$L/up2.log" | tail -25

step "S6 close the terminal: SIGHUP up's process group"
if kill -0 $PID 2>/dev/null; then
  kill -HUP -- -$PID; wait_exit $PID 120
  obs "last lines of up after SIGHUP:"; tail -4 "$L/up2.log"
  obs "status after SIGHUP: $(/tmp/devenv status | grep '^a ' | tr -s ' ')"
  obs "socket after SIGHUP: $(test -S "$(sock)" && echo present || echo gone); kubeconfigs on disk: $(ls .devenv/a/*.kubeconfig 2>/dev/null | wc -l)"
  sleep 10; obs "sessions 10s after SIGHUP: $(sessions)"
fi

step "S7 Ctrl-C during bring-up (SIGINT to the process group during the management cluster stage)"
start_up "$L/up3.log" up --name a
if wait_log "$L/up3.log" "kapp-controller\|cluster api\|management cluster" 900; then
  sleep 10
  obs "stage at Ctrl-C: $(grep -o '\] .*' "$L/up3.log" | tail -1)"
  kill -INT -- -$PID; t=$SECONDS; wait_exit $PID 300; summary+=("Ctrl-C during bring-up exited after $((SECONDS - t))s")
  obs "output after Ctrl-C:"; tail -12 "$L/up3.log"
  obs "status after Ctrl-C: $(/tmp/devenv status | grep '^a ' | tr -s ' '); logs dir: $(ls .devenv/a/logs 2>/dev/null | tr '\n' ' ')"
  obs "sessions: $(sessions)"
fi

step "S8 Ctrl-C twice during bring-up"
start_up "$L/up4.log" up --name a
if wait_log "$L/up4.log" "management cluster" 900; then
  sleep 10
  kill -INT -- -$PID; sleep 1; kill -INT -- -$PID; t=$SECONDS; wait_exit $PID 120
  obs "output after double Ctrl-C:"; tail -6 "$L/up4.log"
  obs "status: $(/tmp/devenv status | grep '^a ' | tr -s ' ')"
  sleep 10; obs "sessions 10s after: $(sessions)"
fi

step "S9 down of a stopped env, then down --purge"
/tmp/devenv down --name a; obs "down of stopped env rc=$?"
t=$SECONDS; /tmp/devenv down --purge --name a; rc=$?
obs "down --purge rc=$rc after $((SECONDS - t))s; dir: $(ls -d .devenv/a 2>&1); socket: $(test -e "$(sock)" && echo present || echo gone)"
disk "after purge"

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
