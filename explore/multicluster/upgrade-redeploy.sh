#!/usr/bin/env bash
# The inner loop while a workload cluster upgrades: redeploy during a Kubernetes minor upgrade, and during a worker rollout of work.
source "$(dirname "$0")/lib.sh"
install_tools
up ur || { summary; exit 1; }

section "blue at v1.36.4"
new_cluster blue default v1.36.4 1 >/dev/null
wait_until "blue (v1.36.4) Available" 720 cluster_available default blue || dump default blue
wait_until "blue PackageInstall reconciled" 420 pkgi_reconciled default blue-greeting-controller

timed_redeploy() { # timed_redeploy VERSION DESC
  local start rc
  start=$(now)
  (cd "$GREETING" && timeout 900 "$DEVENV" redeploy --name ur --version "$1" >"$RUNNER_TEMP/rd.log" 2>&1); rc=$?
  obs "redeploy --version $1 $2: exit $rc after $(since "$start")s; $(tail -3 "$RUNNER_TEMP/rd.log" | tr '\n' ' ' | cut -c1-400)"
}
timed_redeploy v2 "baseline, 2 clusters idle"

section "redeploy during blue's upgrade to v1.37.0"
kubectl -n default patch cluster blue --type merge -p '{"spec":{"topology":{"version":"v1.37.0"}}}'
pause 15
timed_redeploy v3 "during blue's control-plane upgrade"
kubectl get apps -A
upgraded() { [ "$(kubectl -n default get machines -l cluster.x-k8s.io/cluster-name=blue -o jsonpath='{.items[*].spec.version}' | tr ' ' '\n' | sort -u)" = v1.37.0 ] && cluster_available default blue; }
wait_until "blue upgraded and Available" 900 upgraded || dump default blue

section "redeploy during a rollout of work's workers"
kubectl -n default patch cluster work --type json -p '[{"op":"replace","path":"/spec/topology/workers/machineDeployments/0/replicas","value":2}]'
pause 10
timed_redeploy v4 "during work's worker scale-up"
obs "work API from host after: $(kubectl --kubeconfig "$WORK" --request-timeout=10s get nodes --no-headers 2>&1 | awk '{print $1"="$2}' | tr '\n' ' ')"
timed_redeploy v5 "idle again"
kubectl get apps -A
down ur
summary
