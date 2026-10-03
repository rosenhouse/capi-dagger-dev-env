#!/usr/bin/env bash
# Shared helpers for the Carvel-team exploration. Source it; do not run it.
TOOL=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
LIB=$TOOL/explore/lib
W=${RUNNER_TEMP:-/tmp}/carvel
A=$W/acme
DEVENV=$W/devenv
NAME=${NAME:-acme}
SUMMARY=$W/summary.txt
mkdir -p "$W"
: >"$SUMMARY"

obs() { echo "OBS: $*"; echo "$*" >>"$SUMMARY"; }
section() { echo; echo "=================== $* ($(date +%T), t=${SECONDS}s)"; }
summary() { echo; echo "=================== SUMMARY"; cat "$SUMMARY"; }

# setup_consumer copies Acme's repo outside this checkout, as its own Git repo that requires the tool from the module proxy.
setup_consumer() {
  cp -a "$TOOL/explore/carvel/consumer" "$A"
  (cd "$A" && git init -q && git add -A && git -c user.email=x@example.com -c user.name=x commit -qm init)
  local t0=$SECONDS
  (cd "$A" && GOWORK=off go build -o "$DEVENV" ./cmd/devenv) || { obs "CLI build failed"; exit 1; }
  obs "CLI build took $((SECONDS - t0))s"
  curl -fsSL -o "$W/kctrl" https://github.com/carvel-dev/kapp-controller/releases/download/v0.60.9/kctrl-linux-amd64 && chmod +x "$W/kctrl" || obs "kctrl download failed"
}

mk() { kubectl --kubeconfig "$A/.devenv/$NAME/mgmt.kubeconfig" "$@"; }
wk() { kubectl --kubeconfig "$A/.devenv/$NAME/workload.kubeconfig" "$@"; }
kc() { "$W/kctrl" --kubeconfig "$A/.devenv/$NAME/mgmt.kubeconfig" --tty=false --color=false "$@"; }

# up_bg starts up and waits for it. Sets UP_PID; returns 1 if up failed.
up_bg() {
  local t0=$SECONDS
  UP_PID=$(cd "$A" && "$LIB/up-bg.sh" "$W/up-$NAME.log" 1500 -- "$DEVENV" up --name "$NAME")
  local rc=$?
  obs "up[$NAME scenario=${DEVENV_SCENARIO:-base}] rc=$rc took $((SECONDS - t0))s"
  [ $rc = 0 ] || { tail -40 "$W/up-$NAME.log"; dump_state; }
  return $rc
}

# up_fg runs up in the foreground, for configs expected to fail. A config that comes up is taken down.
up_fg() {
  local label=$1 t0=$SECONDS
  (cd "$A" && timeout 1500 "$DEVENV" up --name "$NAME") >"$W/up-$label.log" 2>&1 &
  local pid=$! up=0
  while kill -0 $pid 2>/dev/null; do
    if grep -q " is up\." "$W/up-$label.log"; then up=1; break; fi
    sleep 5
  done
  if [ $up = 1 ]; then
    obs "up[$label] came UP after $((SECONDS - t0))s"
    (cd "$A" && "$DEVENV" down --name "$NAME") >/dev/null 2>&1
    wait $pid
  else
    wait $pid
    obs "up[$label] exited rc=$? after $((SECONDS - t0))s"
  fi
  sed 's/^/    | /' "$W/up-$label.log" | tail -40
}

# rd LABEL [ARGS...] runs redeploy, times it and prints its output.
rd() {
  local label=$1; shift
  local t0=$SECONDS
  (cd "$A" && timeout 1000 "$DEVENV" redeploy --name "$NAME" "$@") >"$W/rd-$label.log" 2>&1
  local rc=$?
  obs "redeploy[$label] rc=$rc took $((SECONDS - t0))s"
  sed 's/^/    | /' "$W/rd-$label.log" | tail -30
  return $rc
}

dump_state() {
  local d=$A/.devenv/$NAME
  ls -la "$d" "$d/logs" 2>/dev/null | head -30
  [ -f "$d/logs/resources.yaml" ] && grep -n -A3 "usefulErrorMessage\|friendlyDescription" "$d/logs/resources.yaml" | head -80
}

down_purge() {
  local t0=$SECONDS
  (cd "$A" && "$DEVENV" down --name "$NAME" --purge) >"$W/down.log" 2>&1
  obs "down --purge rc=$? took $((SECONDS - t0))s"
  [ -n "${UP_PID:-}" ] && wait "$UP_PID" 2>/dev/null
}
