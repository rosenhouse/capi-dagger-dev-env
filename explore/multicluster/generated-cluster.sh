#!/usr/bin/env bash
# Extra clusters made the CAPI-documented way: clusterctl generate cluster, and MachinePool workers.
# Do their nodes pull the consumer's images from the session registry?
source "$(dirname "$0")/lib.sh"
install_tools
up gc || { summary; exit 1; }

copy_cni() { # copy_cni NS: the env's kindnet ConfigMap and ClusterResourceSet, copied into NS
  kubectl -n default get configmap kindnet -o json | jq --arg ns "$1" '{apiVersion, kind, metadata: {name: .metadata.name, namespace: $ns}, data}' | kubectl apply -f - >/dev/null
  kubectl -n default get clusterresourceset kindnet -o json | jq --arg ns "$1" '{apiVersion, kind, metadata: {name: .metadata.name, namespace: $ns}, spec}' | kubectl apply -f - >/dev/null
}

section "B1: clusterctl generate cluster team-b/green, as the CAPD quick start does"
kubectl create namespace team-b
copy_cni team-b
POD_CIDR='["192.168.0.0/16"]' clusterctl generate cluster green --infrastructure docker:v1.14.2 --flavor development \
  --kubernetes-version v1.37.0 --control-plane-machine-count 1 --worker-machine-count 1 --target-namespace team-b >"$RUNNER_TEMP/green.yaml"
obs "clusterctl generate output kinds: $(grep '^kind:' "$RUNNER_TEMP/green.yaml" | sort | uniq -c | awk '{print $3"x"$1}' | tr '\n' ' ')"
kubectl apply -f "$RUNNER_TEMP/green.yaml" | tail -3
kubectl -n team-b label cluster green cni=kindnet
obs "green classRef: $(kubectl -n team-b get cluster green -o jsonpath='{.spec.topology.classRef}')"
wait_until "team-b/green Available" 600 cluster_available team-b green || dump team-b green
kubectl -n team-b get devmachines -o json | jq -r '.items[] | "OBS: green DevMachine \(.metadata.name) mounts=[\([.spec.backend.docker.extraMounts[]?.containerPath] | join(","))]"'
if ! wait_until "PackageInstall team-b/green-greeting-controller reconciled" 360 pkgi_reconciled team-b green-greeting-controller; then
  obs "green-greeting-controller: $(kubectl -n team-b get packageinstall green-greeting-controller -o jsonpath='{.status.usefulErrorMessage}' | tr '\n' ' ' | cut -c1-300)"
fi
if reach green team-b 17445; then
  kubectl --kubeconfig "$RUNNER_TEMP/green.kubeconfig" -n greeting-controller get pods -o wide
  obs "green greeting-controller pod: $(kubectl --kubeconfig "$RUNNER_TEMP/green.kubeconfig" -n greeting-controller get pods -o jsonpath='{range .items[*]}{.status.containerStatuses[0].state}{end}' | cut -c1-300)"
  kubectl --kubeconfig "$RUNNER_TEMP/green.kubeconfig" -n greeting-controller get events --sort-by=.lastTimestamp | tail -6
fi

section "B2: MachinePool workers on default/mp"
new_cluster mp default v1.37.0 1 >/dev/null
wait_until "default/mp Available" 600 cluster_available default mp || dump default mp
reach mp default 17446
wait_until "PackageInstall default/mp-greeting-controller reconciled" 360 pkgi_reconciled default mp-greeting-controller
kubectl -n default patch cluster mp --type merge -p '{"spec":{"topology":{"workers":{"machinePools":[{"class":"default-worker","name":"mp-0","replicas":1}]}}}}'
three_nodes() { [ "$(kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" --request-timeout=10s get nodes --no-headers | grep -c ' Ready ')" = 3 ]; }
wait_until "mp has a third Ready node (MachinePool)" 600 three_nodes
kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" get nodes
kubectl -n default get machinepools,devmachinepools -o wide
kubectl -n default get devmachinepools -o json | jq -c '.items[] | {name: .metadata.name, template: .spec.template}'
kubectl -n default get devmachinepooltemplates -o json | jq -c '.items[] | {name: .metadata.name, template: .spec.template.spec.template}'
mpnode=$(kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" get nodes --no-headers | grep -v -E 'control-plane|md-0' | awk '{print $1}' | head -1)
img=$(kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" -n greeting-controller get deploy greeting-controller -o jsonpath='{.spec.template.spec.containers[0].image}')
echo "MachinePool node $mpnode, session image $img"
kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" -n default run mp-pull --image="$img" --restart=Never \
  --overrides="{\"apiVersion\":\"v1\",\"spec\":{\"nodeName\":\"$mpnode\"}}" -- --help >/dev/null
kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" -n default run md-pull --image="$img" --restart=Never \
  --overrides="{\"apiVersion\":\"v1\",\"spec\":{\"nodeName\":\"$(kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" get nodes --no-headers | grep md-0 | awk '{print $1}')\"}}" -- --help >/dev/null
pause 90
for p in mp-pull md-pull; do
  obs "$p (session-registry image): $(kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" -n default get pod $p -o jsonpath='{.spec.nodeName} {.status.containerStatuses[0].state}' | cut -c1-300)"
done
kubectl --kubeconfig "$RUNNER_TEMP/mp.kubeconfig" -n default describe pod mp-pull | tail -6
obs "MachinePool status: $(kubectl -n default get machinepools -o jsonpath='{range .items[*]}{.metadata.name} phase={.status.phase} ready={.status.readyReplicas}{"\n"}{end}')"

section "Final"
kubectl get clusters -A
down gc
summary
