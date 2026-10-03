#!/usr/bin/env bash
# Another Dagger version on the same machine, such as a second consumer repo that pins an older tool,
# or the dagger CLI used for another project. Run from examples/greeting.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()
OTHER=${OTHER:-0.21.9}

step "V1 up --name a"
start_up "$L/a.log" up --name a; A=$PID
wait_log "$L/a.log" " is up\." 1200 || { tail_state a; exit 1; }
kc .devenv/a/mgmt.kubeconfig get nodes
obs "engines: $(docker ps -a --filter name=dagger-engine --format '{{.Names}}={{.Status}}' | tr '\n' ' ')"
obs "volumes: $(docker volume ls --format '{{.Name}}' | tr '\n' ' ')"
disk "a up"

step "V2 run dagger v$OTHER, as another project would"
curl -fsSL https://dl.dagger.io/dagger/install.sh | DAGGER_VERSION=$OTHER BIN_DIR=/tmp/other sh >/dev/null 2>&1
t=$SECONDS
DAGGER_NO_NAG=1 timeout 600 /tmp/other/dagger -s core version 2>&1 | tail -2
obs "dagger v$OTHER ran in $((SECONDS - t))s"
sleep 5
obs "engines: $(docker ps -a --filter name=dagger-engine --format '{{.Names}}={{.Status}}' | tr '\n' ' ')"
obs "volumes: $(docker volume ls --format '{{.Name}}' | tr '\n' ' ')"
obs "up a alive: $(kill -0 $A 2>/dev/null && echo yes || echo no)"
obs "up a output tail:"; tail -6 "$L/a.log"
obs "status: $(/tmp/devenv status | grep '^a ' | tr -s ' ')"
obs "kubectl a: $(kc .devenv/a/mgmt.kubeconfig get nodes | tail -1)"
obs "redeploy a: $(timeout 300 /tmp/devenv redeploy --name a 2>&1 | tail -1)"
summary+=("after dagger v$OTHER ran: up a alive=$(kill -0 $A 2>/dev/null && echo yes || echo no)")
disk "after other version"

step "V3 up --name a again"
kill -0 $A 2>/dev/null && { kill -INT -- -$A; wait_exit $A 120; }
t=$SECONDS
start_up "$L/a2.log" up --name a
wait_log "$L/a2.log" " is up\." 1200 && summary+=("re-up after other version: $((SECONDS - t))s") || tail_state a
obs "engines: $(docker ps -a --filter name=dagger-engine --format '{{.Names}}={{.Status}}' | tr '\n' ' ')"
obs "volumes: $(docker volume ls --format '{{.Name}}' | tr '\n' ' ')"
docker system df -v 2>&1 | sed -n '/VOLUME NAME/,/^$/p'
disk "a up again"
kill -INT -- -$PID; wait_exit $PID 120

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
