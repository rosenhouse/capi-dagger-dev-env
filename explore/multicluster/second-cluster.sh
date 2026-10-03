#!/usr/bin/env bash
# A second and third workload cluster in a running environment, as a multi-cluster controller developer would add them.
source "$(dirname "$0")/lib.sh"
install_tools
up mc || { summary; exit 1; }

section "What the environment offers"
kubectl get clusterclasses -A
kubectl get clusterresourcesets -A
kubectl -n default get cluster work -o yaml | sed -n '/^metadata:/,/^status:/p' | grep -v -E 'managedFields|f:|uid|resourceVersion' | head -60
obs "work Cluster labels: $(kubectl -n default get cluster work -o jsonpath='{.metadata.labels}')"
obs "work kubeconfig secret server: $(kubectl -n default get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d | grep server: | head -1)"
(cd "$GREETING" && "$DEVENV" status)
resources

section "A1: copy of work as default/work2"
start=$(now)
new_cluster work2 default v1.37.0 1
wait_until "work2 Available" 900 cluster_available default work2 || dump default work2
obs "work2 (copy in default) Available took $(since "$start")s"
resources
(cd "$GREETING" && "$DEVENV" kubeconfig --cluster work2 >/dev/null 2>"$RUNNER_TEMP/kc.err"); obs "devenv kubeconfig --cluster work2: exit $? $(cat "$RUNNER_TEMP/kc.err")"
(cd "$GREETING" && "$DEVENV" status)
clusterctl get kubeconfig work2 -n default >"$RUNNER_TEMP/work2.direct" 2>&1
obs "clusterctl get kubeconfig work2 server: $(grep server: "$RUNNER_TEMP/work2.direct")"
timeout 30 kubectl --kubeconfig "$RUNNER_TEMP/work2.direct" --request-timeout=10s get nodes >"$RUNNER_TEMP/direct.out" 2>&1
obs "kubectl with work2's own kubeconfig from host: exit $? $(tail -1 "$RUNNER_TEMP/direct.out")"
if reach work2 default 17443; then
  kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" get nodes -o wide
  obs "work2 nodes via socat pod + port-forward: $(kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" get nodes --no-headers 2>&1 | awk '{print $1"="$2}' | tr '\n' ' ')"
else
  obs "socat pod + port-forward workaround failed"
fi
kubectl -n default get devmachines -o json | jq -r '.items[] | "\(.metadata.name) mounts=\([.spec.backend.docker.extraMounts[]?.containerPath] | join(","))"'
wait_until "PackageInstall default/work2-greeting-controller reconciled" 420 pkgi_reconciled default work2-greeting-controller ||
  kubectl -n default get packageinstall work2-greeting-controller -o yaml | tail -30
kubectl -n default get packageinstalls,apps
greeting default e2e2 work2 "hello from work2"
wait_until "work2 serves its Greeting (via workaround kubeconfig)" 300 serves "$RUNNER_TEMP/work2.kubeconfig" e2e2 "hello from work2" ||
  kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" get pods -A -o wide
kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" get pods -n default -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.containers[*].image}{"\n"}{end}'

section "A1b: MachinePool workers on work2"
kubectl -n default patch cluster work2 --type merge -p '{"spec":{"topology":{"workers":{"machinePools":[{"class":"default-worker","name":"mp-0","replicas":1}]}}}}' 2>&1 | tail -2
pause 30
kubectl -n default get machinepools,devmachinepools 2>&1 | head
mp_node_ready() { kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" get nodes --no-headers | grep -E -- '-mp-0-' | grep -q ' Ready'; }
if wait_until "work2 MachinePool node Ready" 420 mp_node_ready; then
  node=$(kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" get nodes --no-headers | grep -E -- '-mp-0-' | awk '{print $1}' | head -1)
  img=$(kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" -n default get deploy e2e2-hello -o jsonpath='{.spec.template.spec.containers[0].image}')
  kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" -n default run mp-pull --image="$img" --restart=Never \
    --overrides="{\"spec\":{\"nodeName\":\"$node\"}}" >/dev/null
  pause 60
  obs "pod with session-registry image $img on MachinePool node: $(kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" -n default get pod mp-pull -o jsonpath='{.status.phase} {.status.containerStatuses[0].state}')"
  kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" -n default describe pod mp-pull | tail -8
  kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" -n kube-system get pods -o wide | grep -- -mp-0-
else
  kubectl -n default get machinepools -o yaml | grep -A3 -E 'conditions|message' | head -40
  kubectl --kubeconfig "$RUNNER_TEMP/work2.kubeconfig" get nodes
fi

section "A2: clusterctl generate cluster into namespace team-a"
kubectl create namespace team-a
POD_CIDR='["192.168.0.0/16"]' clusterctl generate cluster blue --infrastructure docker:v1.14.2 --flavor development \
  --kubernetes-version v1.36.4 --control-plane-machine-count 1 --worker-machine-count 1 --target-namespace team-a \
  >"$RUNNER_TEMP/blue.yaml" 2>"$RUNNER_TEMP/blue.err"
obs "clusterctl generate cluster (from GitHub template): exit $? $(tail -2 "$RUNNER_TEMP/blue.err")"
kubectl apply -f "$RUNNER_TEMP/blue.yaml" 2>&1 | tail -3
pause 30
obs "team-a/blue as generated, TopologyReconciled: $(condition team-a blue TopologyReconciled)"
kubectl -n team-a patch cluster blue --type merge -p '{"spec":{"topology":{"classRef":{"namespace":"default"}}}}' 2>&1 | tail -2
start=$(now)
if ! wait_until "team-a/blue (class from default, no CNI label) Available" 600 cluster_available team-a blue; then
  dump team-a blue
fi
obs "team-a/blue Available: $(condition team-a blue Available)"
kubectl -n team-a get packageinstalls,apps 2>&1
section "A3: redeploy while team-a/blue lacks a CNI"
redeploy mc "with a CNI-less extra cluster"
kubectl get apps -A

section "A4: give team-a/blue a CNI the way the env does"
kubectl -n default get configmap kindnet -o json | jq '{apiVersion, kind, metadata: {name: .metadata.name, namespace: "team-a"}, data}' | kubectl apply -f -
kubectl -n default get clusterresourceset kindnet -o json | jq '{apiVersion, kind, metadata: {name: .metadata.name, namespace: "team-a"}, spec}' | kubectl apply -f -
kubectl -n team-a label cluster blue cni=kindnet --overwrite
wait_until "team-a/blue Available after CNI" 600 cluster_available team-a blue || dump team-a blue
wait_until "PackageInstall team-a/blue-greeting-controller reconciled" 600 pkgi_reconciled team-a blue-greeting-controller ||
  kubectl -n team-a get packageinstall blue-greeting-controller -o jsonpath='{.status.usefulErrorMessage}'
if reach blue team-a 17444; then
  obs "blue nodes: $(kubectl --kubeconfig "$RUNNER_TEMP/blue.kubeconfig" get nodes --no-headers 2>&1 | awk '{print $1"="$2"/"$5}' | tr '\n' ' ')"
  greeting team-a e2e3 blue "hello from blue"
  wait_until "team-a/blue serves its Greeting" 300 serves "$RUNNER_TEMP/blue.kubeconfig" e2e3 "hello from blue"
fi
redeploy mc "with 3 healthy clusters"
resources

section "A5: upgrade team-a/blue v1.36.4 -> v1.37.0"
start=$(now)
kubectl -n team-a patch cluster blue --type merge -p '{"spec":{"topology":{"version":"v1.37.0"}}}'
upgraded() { [ "$(kubectl --kubeconfig "$RUNNER_TEMP/blue.kubeconfig" --request-timeout=10s get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}' | tr ' ' '\n' | sort -u)" = v1.37.0 ] && cluster_available team-a blue; }
wait_until "blue upgraded to v1.37.0 (all nodes) and Available" 900 upgraded || { dump team-a blue; kubectl --kubeconfig "$RUNNER_TEMP/blue.kubeconfig" get nodes; }
obs "blue's workaround kubeconfig after upgrade: $(kubectl --kubeconfig "$RUNNER_TEMP/blue.kubeconfig" --request-timeout=10s get nodes --no-headers 2>&1 | head -3 | tr '\n' ' ')"
wait_until "blue serves Greeting after upgrade" 300 serves "$RUNNER_TEMP/blue.kubeconfig" e2e3 "hello from blue"
kubectl get apps -A

section "Final state"
(cd "$GREETING" && "$DEVENV" status)
kubectl get clusters -A
resources
down mc
summary
