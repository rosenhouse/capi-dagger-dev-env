#!/usr/bin/env bash
# Step 0: the tool for clusters only, with fleet-addons deployed by the team's own tooling.
source "$(dirname "$0")/lib.sh"
T=$RUNNER_TEMP
B=$T/step0
copy_repo "$B"; devenv_module "$B"; vendor_repo "$B/repo"
cd "$B/repo/hack/devenv" || exit 1
k() { timeout 60 kubectl --request-timeout=30s "$@"; }

export DEVENV_SCENARIO=clusters-only
start=$SECONDS
if ! pid=$("$TOOL/explore/lib/up-bg.sh" "$T/step0-up.log" 1200 -- "$B/devenv" up --name step0); then
  obs "clusters-only up failed: $(grep -m1 '^Error' "$T/step0-up.log")"
  state_tail "$B/repo/hack/devenv" step0
  summary; exit 1
fi
obs "clusters-only up took $((SECONDS - start))s"
D=$B/repo/hack/devenv/.devenv/step0
export KUBECONFIG=$D/mgmt.kubeconfig
W=$D/workload.kubeconfig
obs "kind get clusters on the host: '$(kind get clusters 2>&1 | tr '\n' ' ')'"
obs "containers in the host's Docker: '$(docker ps --format '{{.Image}}' | tr '\n' ' ')'"
obs "Cluster CRD versions: $(k get crd clusters.cluster.x-k8s.io -o jsonpath='{range .spec.versions[*]}{.name}:served={.served},storage={.storage} {end}')"
obs "kubectl get clusters.v1beta1: $(k get clusters.v1beta1.cluster.x-k8s.io -A 2>&1 | tail -2 | tr '\n' ' ')"
obs "cert-manager on mgmt: $(k get deploy -n cert-manager -o name 2>&1 | tr '\n' ' ')"
obs "cert-manager CRD on workload: $(k --kubeconfig "$W" get crd certificates.cert-manager.io -o name 2>&1)"
obs "devenv status: $("$B/devenv" status | tr -s ' ' | tr '\n' ';')"
obs "git status shows: $(git -C "$B/repo" status --porcelain | tr '\n' ' ')"

cd "$B/repo" || exit 1
echo "--- make deploy with a host-built image"
start=$SECONDS
timeout 600 docker build -q --build-arg VERSION=hostbuild -t controller:latest . >/dev/null 2>"$T/docker-build.log" || { obs "docker build failed"; tail -20 "$T/docker-build.log"; }
obs "docker build of the team's Dockerfile took $((SECONDS - start))s"
k kustomize config/default | k apply -f - 2>&1 | tail -3
k -n fleet-addons-system rollout status deploy/fleet-addons-controller-manager --timeout=50s >/dev/null 2>&1
obs "with controller:latest built on the host, the manager pod is: $(k -n fleet-addons-system get pods -o jsonpath='{range .items[*]}{.status.phase}/{.status.containerStatuses[0].state.waiting.reason} {end}')"
k -n fleet-addons-system get events --field-selector reason=Failed -o jsonpath='{range .items[*]}{.message}{"\n"}{end}' | head -2

echo "--- push to a public ephemeral registry instead"
IMG=ttl.sh/acme-fleet-addons-${GITHUB_RUN_ID:-local}-$RANDOM:2h
start=$SECONDS
docker tag controller:latest "$IMG" && timeout 300 docker push -q "$IMG" >/dev/null && obs "pushed $IMG in $((SECONDS - start))s"
k kustomize config/default | sed "s#image: controller:latest#image: $IMG#" | k apply -f - >/dev/null
start=$SECONDS
if k -n fleet-addons-system rollout status deploy/fleet-addons-controller-manager --timeout=50s; then
  obs "manager from ttl.sh rolled out in $((SECONDS - start))s"
else
  obs "manager from ttl.sh did not roll out: $(k -n fleet-addons-system get pods 2>&1 | tail -2 | tr '\n' ' ')"
  k -n fleet-addons-system describe pods | tail -20
fi
k -n fleet-addons-system logs deploy/fleet-addons-controller-manager --tail=8 2>&1
obs "manager log lines mentioning deprecation: $(k -n fleet-addons-system logs deploy/fleet-addons-controller-manager 2>&1 | grep -ci 'deprecat')"

echo "--- webhook and samples"
for i in $(seq 1 20); do k apply -n default -f config/samples/fleet_v1alpha1_fleetpolicy.yaml >"$T/sample.log" 2>&1 && break; sleep 6; done
obs "valid FleetPolicy: $(tr '\n' ' ' <"$T/sample.log")"
obs "invalid FleetPolicy: $(printf 'apiVersion: fleet.acme.io/v1alpha1\nkind: FleetPolicy\nmetadata:\n  name: bad\n  namespace: default\nspec:\n  message: ""\n' | k create -f - 2>&1 | tr '\n' ' ')"

echo "--- Ginkgo e2e from the host"
start=$SECONDS
MGMT_KUBECONFIG=$KUBECONFIG WORKLOAD_KUBECONFIG=$W timeout 400 go test ./test/e2e/ -count=1 -v -ginkgo.v >"$T/e2e.log" 2>&1
obs "e2e from host exit=$? in $((SECONDS - start))s: $(grep -E 'fleet-info version|Ran [0-9]+ of|FAIL!|SUCCESS!' "$T/e2e.log" | tr '\n' ' ')"
tail -15 "$T/e2e.log"

echo "--- the workload kubeconfig CAPI stores, as e2e frameworks read it"
k get secret -n default work-kubeconfig -o jsonpath='{.data.value}' | base64 -d >"$T/work-secret.kubeconfig"
obs "server in CAPI's work-kubeconfig secret: $(grep -m1 'server:' "$T/work-secret.kubeconfig" | tr -s ' ')"
obs "kubectl with it from the host: $(timeout 30 kubectl --kubeconfig "$T/work-secret.kubeconfig" --request-timeout=10s get nodes 2>&1 | tail -1 | cut -c1-200)"
obs "server in devenv's workload.kubeconfig: $(grep -m1 'server:' "$W" | tr -s ' ')"

echo "--- port-forward from the host"
kubectl -n fleet-addons-system port-forward svc/fleet-addons-webhook-service 19443:443 >"$T/pf.log" 2>&1 &
pf=$!
"$TOOL/explore/lib/pause.sh" 6
obs "port-forward to the webhook service: HTTP $(curl -m 10 -sk -o /dev/null -w '%{http_code}' -X POST https://127.0.0.1:19443/validate-fleet-acme-io-v1alpha1-fleetpolicy 2>&1)"
kill $pf 2>/dev/null

stop_up "$pid"
summary
