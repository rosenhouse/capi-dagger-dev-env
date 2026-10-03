#!/usr/bin/env bash
# Shared helpers for the inner-loop exploration. Source it; do not run it.
TOOL=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
LIB=$TOOL/explore/lib
W=${RUNNER_TEMP:-/tmp}/w
G=$W/greeting
DEVENV=$W/devenv
NAME=${NAME:-il}
SUMMARY=$W/summary.txt
mkdir -p "$W"
: >"$SUMMARY"

obs() { echo "OBS: $*"; echo "$*" >>"$SUMMARY"; }
section() { echo; echo "=================== $* ($(date +%T), t=${SECONDS}s)"; }

# setup_greeting copies the greeting example outside the repo, with a go.work that builds the CLI against this checkout,
# as the repository's own go.work does. The in-container build reads only the copy's go.mod.
setup_greeting() {
  cp -a "$TOOL/examples/greeting" "$G"
  cat >"$W/go.work" <<EOF
go 1.26.1

use (
	./greeting
	$TOOL
)
EOF
  local t0=$SECONDS
  (cd "$G" && go build -o "$DEVENV" ./cmd/devenv) || { obs "CLI build failed"; exit 1; }
  obs "CLI build took $((SECONDS - t0))s"
}

mk() { kubectl --kubeconfig "$G/.devenv/$NAME/mgmt.kubeconfig" "$@"; }
wk() { kubectl --kubeconfig "$G/.devenv/$NAME/workload.kubeconfig" "$@"; }

# up_bg starts up and waits for it. Sets UP_PID.
up_bg() {
  local t0=$SECONDS
  cd "$G" || exit 1
  if UP_PID=$("$LIB/up-bg.sh" "$W/up.log" 1500 -- "$DEVENV" up --name "$NAME"); then
    obs "up took $((SECONDS - t0))s (pid $UP_PID)"
  else
    obs "up FAILED after $((SECONDS - t0))s"
    tail -60 "$W/up.log"
    dump_state
    summary
    exit 1
  fi
}

# rd LABEL [ARGS...] runs redeploy, times it and prints its output.
rd() {
  local label=$1; shift
  local t0=$SECONDS
  (cd "$G" && timeout 1000 "${RD_BIN:-$DEVENV}" redeploy --name "$NAME" "$@") >"$W/rd-$label.log" 2>&1
  local rc=$?
  obs "redeploy[$label] rc=$rc took $((SECONDS - t0))s"
  sed 's/^/    | /' "$W/rd-$label.log" | tail -40
  return $rc
}

# snap FILE records the pods of the example's components in both clusters.
snap() {
  {
    for ns in addon-manager greeting-syncer; do
      mk -n $ns get pods --no-headers -o custom-columns='N:.metadata.name,R:.status.containerStatuses[0].restartCount,P:.status.phase' 2>&1 | sed "s|^|mgmt/$ns/|"
    done
    for ns in greeting-controller default; do
      wk -n $ns get pods --no-headers -o custom-columns='N:.metadata.name,R:.status.containerStatuses[0].restartCount,P:.status.phase' 2>&1 | sed "s|^|work/$ns/|"
    done
  } | sort >"$1"
}

# rolled BEFORE AFTER prints which pods were replaced.
rolled() {
  local gone new
  gone=$(comm -23 <(awk '{print $1}' "$1") <(awk '{print $1}' "$2") | tr '\n' ' ')
  new=$(comm -13 <(awk '{print $1}' "$1") <(awk '{print $1}' "$2") | tr '\n' ' ')
  echo "gone=[$gone] new=[$new]"
}

greeting() {
  mk apply -f - <<EOF
apiVersion: demo.example.com/v1alpha1
kind: Greeting
metadata: {name: e2e, namespace: default}
spec: {clusterName: work, message: "$1"}
EOF
}

hello_body() { wk get --raw /api/v1/namespaces/default/services/http:e2e-proxy:80/proxy/ 2>&1 | head -3; }

# wait_hello PATTERN TIMEOUT waits until hello's body matches PATTERN; prints how long.
wait_hello() {
  local t0=$SECONDS end=$((SECONDS + $2)) body
  while [ $SECONDS -lt $end ]; do
    body=$(hello_body)
    if echo "$body" | grep -q -- "$1"; then echo "$((SECONDS - t0))"; return 0; fi
    sleep 2
  done
  echo "timeout; last body: $body"
  return 1
}

dump_state() {
  section "state dump"
  ls -la "$G/.devenv/$NAME" 2>&1
  tail -40 "$G/.devenv/$NAME/dagger.log" 2>&1
  mk get apps,pkgi -A 2>&1 | head -20
}

summary() {
  section "SUMMARY"
  cat "$SUMMARY"
}
