#!/usr/bin/env bash
# Shared helpers for the multicluster exploration. Source it.
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
GREETING=$REPO/examples/greeting
BIN=$RUNNER_TEMP/bin
export PATH=$BIN:$PATH
DEVENV=$BIN/devenv
SUMMARY=()

obs() { echo "OBS: $*"; SUMMARY+=("$*"); }
now() { date +%s; }
since() { echo $(( $(date +%s) - $1 )); }
pause() { "$REPO/explore/lib/pause.sh" "$1"; }
section() { echo; echo "=================== $* ==================="; }

summary() {
  section SUMMARY
  printf '  - %s\n' "${SUMMARY[@]}"
}

install_tools() {
  mkdir -p "$BIN"
  curl -fsSLo "$BIN/kubectl" https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl
  curl -fsSLo "$BIN/clusterctl" https://github.com/kubernetes-sigs/cluster-api/releases/download/v1.14.2/clusterctl-linux-amd64
  chmod +x "$BIN/kubectl" "$BIN/clusterctl"
  (cd "$GREETING" && go build -o "$DEVENV" ./cmd/devenv)
}

# up NAME brings up the greeting environment in the background and exports KUBECONFIG for its management cluster.
up() {
  local name=$1 start
  start=$(now)
  cd "$GREETING" || return 1
  if ! UP_PID=$("$REPO/explore/lib/up-bg.sh" "$RUNNER_TEMP/up-$name.log" 1500 -- "$DEVENV" up --name "$name"); then
    obs "up $name FAILED after $(since "$start")s"
    tail -60 "$RUNNER_TEMP/up-$name.log"
    return 1
  fi
  obs "up $name took $(since "$start")s"
  grep -E '^\[' "$RUNNER_TEMP/up-$name.log" | tail -30
  ENVDIR=$GREETING/.devenv/$name
  MGMT=$ENVDIR/mgmt.kubeconfig
  WORK=$ENVDIR/workload.kubeconfig
  export KUBECONFIG=$MGMT
}

down() {
  local name=$1 start
  start=$(now)
  (cd "$GREETING" && timeout 600 "$DEVENV" down --name "$name")
  obs "down $name exit $? after $(since "$start")s"
}

# wait_until DESC TIMEOUT_SECONDS CMD... runs CMD every 10s until it succeeds.
wait_until() {
  local desc=$1 timeout=$2 start
  shift 2
  start=$(now)
  while true; do
    if "$@" >/dev/null 2>&1; then
      obs "$desc: yes after $(since "$start")s"
      return 0
    fi
    if [ "$(since "$start")" -ge "$timeout" ]; then
      obs "$desc: NO after ${timeout}s"
      return 1
    fi
    sleep 10
  done
}

condition() { # condition NS NAME TYPE prints status and message of a Cluster condition
  kubectl -n "$1" get cluster "$2" -o jsonpath="{.status.conditions[?(@.type==\"$3\")].status} {.status.conditions[?(@.type==\"$3\")].message}"
}
cluster_available() { [ "$(kubectl -n "$1" get cluster "$2" -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')" = True ]; }
pkgi_reconciled() { [ "$(kubectl -n "$1" get packageinstall "$2" -o jsonpath='{.status.conditions[?(@.type=="ReconcileSucceeded")].status}')" = True ]; }

# new_cluster NAME NS VERSION [WORKERS] applies a copy of the work Cluster's topology, as a user might.
new_cluster() {
  local name=$1 ns=$2 version=$3 workers=${4:-1}
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n default get cluster work -o json | jq --arg n "$name" --arg ns "$ns" --arg v "$version" --argjson w "$workers" '
    {apiVersion, kind,
     metadata: {name: $n, namespace: $ns, labels: (.metadata.labels // {} | with_entries(select(.key | contains("cluster.x-k8s.io") | not)))},
     spec: (.spec | del(.controlPlaneEndpoint, .infrastructureRef, .controlPlaneRef)
            | .topology.version = $v | .topology.workers.machineDeployments[0].replicas = $w)}' |
    kubectl apply -f -
}

# reach NAME NS PORT writes $RUNNER_TEMP/NAME.kubeconfig, reaching the cluster's API from the host
# through a socat pod in the management cluster and kubectl port-forward: the only route a user has.
reach() {
  local name=$1 ns=$2 port=$3 kc=$RUNNER_TEMP/$1.kubeconfig server
  kubectl -n "$ns" get secret "$name-kubeconfig" -o jsonpath='{.data.value}' | base64 -d >"$kc.orig"
  server=$(kubectl --kubeconfig "$kc.orig" config view -o jsonpath='{.clusters[0].cluster.server}')
  echo "secret server for $name: $server"
  kubectl -n default delete pod "fwd-$name" --ignore-not-found --wait=true >/dev/null 2>&1
  kubectl -n default run "fwd-$name" --image=alpine/socat:1.8.0.3 --restart=Never -- \
    TCP-LISTEN:6443,fork,reuseaddr "TCP:${server#https://}" >/dev/null
  kubectl -n default wait --for=condition=Ready "pod/fwd-$name" --timeout=180s >/dev/null || return 1
  pkill -f "port-forward pod/fwd-$name" 2>/dev/null
  kubectl -n default port-forward "pod/fwd-$name" "$port:6443" >"$RUNNER_TEMP/pf-$name.log" 2>&1 &
  sleep 3
  cp "$kc.orig" "$kc"
  kubectl --kubeconfig "$kc" config set-cluster "$(kubectl --kubeconfig "$kc" config view -o jsonpath='{.clusters[0].name}')" \
    --server "https://127.0.0.1:$port" >/dev/null
}

greeting() { # greeting NS NAME CLUSTER MESSAGE
  kubectl apply -f - <<EOF
apiVersion: demo.example.com/v1alpha1
kind: Greeting
metadata: {name: $2, namespace: $1}
spec: {clusterName: $3, message: "$4"}
EOF
}
serves() { # serves KUBECONFIG GREETING MESSAGE
  kubectl --kubeconfig "$1" --request-timeout=10s get --raw "/api/v1/namespaces/default/services/$2-proxy:80/proxy/" 2>/dev/null | grep -q "^$3 (hello"
}

resources() {
  echo "--- resources: $(cut -d' ' -f1-3 /proc/loadavg) load; $(free -m | awk '/Mem/{print $3"MiB used, "$7"MiB available"}'); disk $(df --output=used -BG / | tail -1)"
}

redeploy() { # redeploy NAME [DESC]: runs devenv redeploy and records its outcome
  local start rc
  start=$(now)
  (cd "$GREETING" && timeout 900 "$DEVENV" redeploy --name "$1" >"$RUNNER_TEMP/redeploy.log" 2>&1)
  rc=$?
  tail -12 "$RUNNER_TEMP/redeploy.log"
  obs "redeploy ${2:-}: exit $rc after $(since "$start")s; last line: $(tail -1 "$RUNNER_TEMP/redeploy.log")"
  return $rc
}

dump() { # dump NS NAME: Cluster-level diagnostics
  echo "--- Cluster $1/$2 conditions"
  kubectl -n "$1" get cluster "$2" -o json | jq -r '.status.conditions[]? | "\(.type)=\(.status) \(.reason // "") \(.message // "" | gsub("\n"; " | "))"' | head -20
  kubectl -n "$1" get machines -l "cluster.x-k8s.io/cluster-name=$2" -o wide 2>&1 | head
}
