#!/usr/bin/env bash
# Several environments at once: commands without --name, down while test runs, random names, disk per environment.
# Run from examples/greeting.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()

step "M1 up --name a and up --name b at once"
disk before
start_up "$L/a.log" up --name a; A=$PID
start_up "$L/b.log" up --name b; B=$PID
sleep 20
obs "test --name a while up a runs: $(/tmp/devenv test --name a 2>&1 | tail -1)"
obs "status while both come up:"; status
PID=$A; wait_log "$L/a.log" " is up\." 1200 || tail_state a
PID=$B; wait_log "$L/b.log" " is up\." 600 || tail_state b
summary+=("a and b up after $((SECONDS - T0))s")
obs "ports a: $(ports a) b: $(ports b)"
disk "a and b up"; obs "sessions: $(sessions)"

step "M2 commands without --name while two run"
for c in "redeploy" "kubeconfig" "down"; do obs "$c without --name: $(timeout 60 /tmp/devenv $c 2>&1 | tail -1)"; done

step "M3 kubeconfigs of two environments merged, as k9s users do"
KUBECONFIG=.devenv/a/mgmt.kubeconfig:.devenv/b/mgmt.kubeconfig:.devenv/a/workload.kubeconfig:.devenv/b/workload.kubeconfig kubectl config get-contexts 2>&1 | sed 's/^/    /'

step "M4 down --name b, then look at what it leaves"
t=$SECONDS; /tmp/devenv down --name b; obs "down b rc=$? after $((SECONDS - t))s"
wait_exit $B 60
obs "status:"; status
obs "b files after down: $(ls .devenv/b | tr '\n' ' ')"
obs "kubectl with b's leftover kubeconfig: $(kc .devenv/b/mgmt.kubeconfig get nodes | tail -1)"
obs "kubeconfig without --name now: $(/tmp/devenv kubeconfig 2>&1 | grep -m1 -o 'current-context: .*')"
obs "kubeconfig --name b: $(/tmp/devenv kubeconfig --name b 2>&1 | tail -1)"
disk "b down"

step "M5 down while test runs"
start_up "$L/c.log" test --name c; C=$PID
PID=$C
if wait_log "$L/c.log" "consumer components" 1200; then
  sleep 45
  t=$SECONDS; out=$(/tmp/devenv down --name c 2>&1); rc=$?
  obs "down c during test: rc=$rc after $((SECONDS - t))s: $(echo "$out" | tr '\n' ' ')"
  wait_exit $C 300
  wait $C 2>/dev/null; obs "test exit code: $?"
  obs "test output tail:"; tail -12 "$L/c.log"
  obs "status:"; status
  summary+=("down during test: down rc=$rc")
fi

step "M6 random-named up, Ctrl-C after it is up"
start_up "$L/r.log" up
if wait_log "$L/r.log" " is up\." 1200; then
  R=$(grep -o 'Environment env-[a-z0-9]*' "$L/r.log" | head -1 | cut -d' ' -f2)
  disk "random $R up"
  kill -INT -- -$PID; wait_exit $PID 120
  obs "random up output tail:"; tail -4 "$L/r.log"
  obs "status after Ctrl-C of random up:"; status
  disk "random $R stopped"
  obs "down --purge without --name: $(/tmp/devenv down --purge 2>&1 | tail -1)"
fi

step "M7 purge a running environment, then the rest, and measure what remains"
t=$SECONDS; /tmp/devenv down --purge --name a; obs "down --purge of running a: rc=$? after $((SECONDS - t))s"
wait_exit $A 60; disk "a purged"
for n in $(ls .devenv); do t=$SECONDS; out=$(/tmp/devenv down --purge --name "$n" 2>&1); obs "purge $n rc=$? $((SECONDS - t))s: $(echo "$out" | tr '\n' ' ')"; disk "$n purged"; done
obs "status:"; status
ls -la .devenv 2>&1 | head
docker system df -v 2>&1 | sed -n '/VOLUME NAME/,/^$/p' | head
cachevols "after purging every environment"

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
