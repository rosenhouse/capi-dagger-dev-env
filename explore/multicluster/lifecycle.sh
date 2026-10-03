#!/usr/bin/env bash
# Scale, delete and recreate the env's own work cluster mid-session, as a lifecycle-aware controller developer would.
source "$(dirname "$0")/lib.sh"
install_tools
up lc || { summary; exit 1; }

# Probe the host's workload kubeconfig every 3s for the rest of the run.
probe() {
  while true; do
    if out=$(kubectl --kubeconfig "$WORK" --request-timeout=5s get --raw /readyz 2>&1); then r=ok; else r=$(echo "$out" | tail -1 | cut -c1-160); fi
    echo "$(date +%s) $(cat "$RUNNER_TEMP/phase") $r"
    sleep 3
  done >"$RUNNER_TEMP/probe.log" 2>&1
}
phase() { echo "$1" >"$RUNNER_TEMP/phase"; section "$1"; }
phase baseline
probe & PROBE=$!

greeting default e2e work "hello from work"
wait_until "work serves Greeting at start" 300 serves "$WORK" e2e "hello from work"
cp "$WORK" "$RUNNER_TEMP/workload.kubeconfig.orig"

phase scale-up
start=$(now)
kubectl -n default patch cluster work --type json -p '[{"op":"replace","path":"/spec/topology/workers/machineDeployments/0/replicas","value":2}]'
three_ready() { [ "$(kubectl --kubeconfig "$WORK" --request-timeout=10s get nodes --no-headers | grep -c ' Ready ')" = 3 ]; }
wait_until "work has 3 Ready nodes after scaling workers to 2" 600 three_ready || { dump default work; kubectl --kubeconfig "$WORK" get nodes; }
kubectl --kubeconfig "$WORK" get nodes -o wide
kubectl --kubeconfig "$WORK" -n kube-system get pods -o wide | grep -E 'kindnet|kube-proxy'

phase scale-down
kubectl -n default patch cluster work --type json -p '[{"op":"replace","path":"/spec/topology/workers/machineDeployments/0/replicas","value":1}]'
two_ready() { [ "$(kubectl --kubeconfig "$WORK" --request-timeout=10s get nodes --no-headers | wc -l)" = 2 ]; }
wait_until "work back to 2 nodes" 600 two_ready
wait_until "work serves Greeting after scaling" 300 serves "$WORK" e2e "hello from work"

phase delete
kubectl -n default get cluster work -o json | jq '{apiVersion, kind,
  metadata: {name: .metadata.name, namespace: .metadata.namespace, labels: (.metadata.labels // {} | with_entries(select(.key | contains("cluster.x-k8s.io") | not)))},
  spec: (.spec | del(.controlPlaneEndpoint, .infrastructureRef, .controlPlaneRef))}' >"$RUNNER_TEMP/work.json"
obs "old work kubeconfig secret server: $(kubectl -n default get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d | grep server:)"
start=$(now)
kubectl -n default delete cluster work --wait=false
gone() { ! kubectl -n default get cluster work >/dev/null 2>&1; }
wait_until "work Cluster deleted" 600 gone
obs "workload kubeconfig after delete: $(kubectl --kubeconfig "$WORK" --request-timeout=10s get nodes 2>&1 | tail -1 | cut -c1-200)"
(cd "$GREETING" && "$DEVENV" status)
(cd "$GREETING" && "$DEVENV" kubeconfig --name lc --cluster workload >"$RUNNER_TEMP/kc.out" 2>&1); obs "devenv kubeconfig --cluster workload after delete: exit $? $(head -c 80 "$RUNNER_TEMP/kc.out" | tr '\n' ' ')"
kubectl get packageinstalls,apps -A
redeploy lc "after work cluster deleted"
kubectl -n devenv get packageinstalls

phase recreate
start=$(now)
kubectl apply -f "$RUNNER_TEMP/work.json"
wait_until "recreated work Available" 900 cluster_available default work || dump default work
obs "recreated work Available took $(since "$start")s"
obs "new work kubeconfig secret server: $(kubectl -n default get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d | grep server:)"
obs "host workload kubeconfig after recreate: $(kubectl --kubeconfig "$WORK" --request-timeout=10s get nodes 2>&1 | tail -1 | cut -c1-200)"
(cd "$GREETING" && "$DEVENV" kubeconfig --name lc --cluster workload >"$RUNNER_TEMP/kc2.out" 2>&1)
cmp -s "$RUNNER_TEMP/kc2.out" "$RUNNER_TEMP/workload.kubeconfig.orig"; obs "devenv kubeconfig --cluster workload after recreate identical to original: $([ $? = 0 ] && echo yes || echo no)"
# A user's repair: the new secret's credentials, pointed at the env's existing tunnel port.
kubectl -n default get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d >"$RUNNER_TEMP/fresh.kubeconfig"
port=$(grep server: "$WORK" | sed -E 's/.*:([0-9]+)$/\1/')
kubectl --kubeconfig "$RUNNER_TEMP/fresh.kubeconfig" config set-cluster "$(kubectl --kubeconfig "$RUNNER_TEMP/fresh.kubeconfig" config view -o jsonpath='{.clusters[0].name}')" --server "https://localhost:$port" >/dev/null
obs "fresh credentials through the old tunnel port $port: $(kubectl --kubeconfig "$RUNNER_TEMP/fresh.kubeconfig" --request-timeout=10s get nodes --no-headers 2>&1 | awk '{print $1"="$2}' | tr '\n' ' ' | cut -c1-200)"
wait_until "PackageInstall work-greeting-controller reconciled again" 600 pkgi_reconciled default work-greeting-controller
wait_until "recreated work serves the existing Greeting (fresh kubeconfig)" 300 serves "$RUNNER_TEMP/fresh.kubeconfig" e2e "hello from work"
redeploy lc "after work cluster recreated"

phase end
kill $PROBE
echo "--- probe outcomes per phase"
awk '{p=$2; $1=$2=""; r=($0 ~ /^ *ok$/) ? "ok" : "fail"; n[p" "r]++} END {for (k in n) print k, n[k]}' "$RUNNER_TEMP/probe.log" | sort
echo "--- distinct probe failures"
awk '{ $1=""; print }' "$RUNNER_TEMP/probe.log" | grep -v ' ok$' | sort | uniq -c | sort -rn | head -15
tail -20 "$RUNNER_TEMP/up-lc.log"
down lc
summary
