#!/usr/bin/env bash
# Job B2: the polyglot consumer on an arm64 runner.
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=$HERE/../lib
T=${RUNNER_TEMP:-/tmp}/polyArm
NAME=arm
STATE=$T/poly/.devenv
DEVENV=$T/devenv
SUMMARY=()
. "$HERE/lib.sh"
export GOWORK=off
mkdir -p "$T"
obs "runner $(uname -m)"
"$LIB/adopter.sh" "$HERE/fixtures/poly" "$T/poly" >/dev/null 2>&1 || obs "adopter.sh failed"
cd "$T/poly" || exit 1
go build -o "$DEVENV" ./cmd/devenv || { obs "devenv main failed to build"; summary; exit 1; }
t=$SECONDS
pid=$("$LIB/up-bg.sh" "$T/up.log" 1500 -- "$DEVENV" up --name "$NAME")
if [ -z "$pid" ]; then obs "up failed after $((SECONDS - t))s"; tail -60 "$T/up.log"; dump_state; summary; exit 1; fi
obs "cold up took $((SECONDS - t))s"
for p in $(kmgmt -n greeter get pods -o name); do kmgmt -n greeter logs "$p" | grep -m1 greeting; done
obs "greeter start line: $(kmgmt -n greeter logs deploy/greeter | grep -m1 greeting)"
obs "manager arch: $(kmgmt get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')"
sed -i 's/hello-v1/hello-v2/' services/greeter/app.py
redeploy
obs "python edit redeploy rc=$RD_RC ${RD_SECS}s"
"$DEVENV" down --name "$NAME"
for _ in $(seq 60); do kill -0 "$pid" 2>/dev/null || break; sleep 2; done
summary
