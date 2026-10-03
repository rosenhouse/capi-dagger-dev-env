#!/usr/bin/env bash
# A team's first attempts: the README path in the controller's own module, the kubebuilder
# config tree as a package, then Step 0: clusters only, deployed with the team's own tooling.
source "$(dirname "$0")/lib.sh"
T=$RUNNER_TEMP

echo "### 1a: README path, devenv main in fleet-addons' own module"
A=$T/same
copy_repo "$A"
cd "$A/repo" || exit 1
git rm -rq hack/devenv && git commit -qm "no nested devenv"
cp -a "$LEG/variants/samemodule/." "$A/repo/"
cp go.mod "$T/go.mod.before"
go build ./... && obs "1a: before adoption, fleet-addons builds"
go mod edit -require=github.com/rosenhouse/capi-dagger-dev-env@v0.0.0-00010101000000-000000000000 \
  -replace=github.com/rosenhouse/capi-dagger-dev-env="$TOOL"
go mod tidy 2>&1 | tail -3
obs "1a: go.mod after adding the tool: $(diff "$T/go.mod.before" go.mod | grep -E '^[<>] (go |\s*(k8s.io/(api|apimachinery|client-go)|sigs.k8s.io/(controller-runtime|cluster-api)) )' | tr -s '\t ' ' ' | tr '\n' ';')"
if go build ./... >"$T/same-build.log" 2>&1; then obs "1a: fleet-addons still builds after adding the tool"
else obs "1a: fleet-addons no longer builds: $(grep -m2 -vE '^#' "$T/same-build.log" | cut -c1-220 | tr '\n' ' ')"; fi
git add -A && git commit -qm devenv
if go build -o "$A/devenv" ./cmd/devenv; then
  expect_fail "$T/same-up.log" 900 -- "$A/devenv" up --name same
  state_tail "$A/repo" same
fi

echo "### 1c: the kubebuilder config tree as the package config"
B=$T/raw
copy_repo "$B"; devenv_module "$B"; vendor_repo "$B/repo"
cd "$B/repo/hack/devenv" || exit 1
export DEVENV_SCENARIO=raw-config
expect_fail "$T/raw-up.log" 1500 -- "$B/devenv" up --name raw
state_tail "$B/repo/hack/devenv" raw

echo "### 1d: Step 0, clusters only"
export DEVENV_SCENARIO=clusters-only
start=$SECONDS
if ! pid=$("$TOOL/explore/lib/up-bg.sh" "$T/step0-up.log" 1500 -- "$B/devenv" up --name step0); then
  obs "1d: clusters-only up failed: $(grep -m1 '^Error' "$T/step0-up.log")"
  state_tail "$B/repo/hack/devenv" step0
  summary; exit 1
fi
obs "1d: clusters-only up took $((SECONDS - start))s"
D=$B/repo/hack/devenv/.devenv/step0
export KUBECONFIG=$D/mgmt.kubeconfig
W=$D/workload.kubeconfig
obs "1d: kind get clusters on the host: '$(kind get clusters 2>&1 | tr '\n' ' ')'"
obs "1d: docker images on the host with kindest: '$(docker ps --format '{{.Image}}' | tr '\n' ' ')'"
obs "1d: Cluster CRD versions: $(kubectl get crd clusters.cluster.x-k8s.io -o jsonpath='{range .spec.versions[*]}{.name}:served={.served},storage={.storage} {end}')"
obs "1d: kubectl get clusters.v1beta1: $(kubectl get clusters.v1beta1.cluster.x-k8s.io -A 2>&1 | tail -2 | tr '\n' ' ')"
obs "1d: cert-manager on mgmt: $(kubectl get deploy -n cert-manager -o name 2>&1 | tr '\n' ' ')"
obs "1d: cert-manager CRD on workload: $(kubectl --kubeconfig "$W" get crd certificates.cert-manager.io -o name 2>&1)"
obs "1d: devenv status: $(cd "$B/repo/hack/devenv" && "$B/devenv" status | tr -s ' ' | tr '\n' ';')"
obs "1d: git status shows: $(git -C "$B/repo" status --porcelain | tr '\n' ' ')"

cd "$B/repo" || exit 1
echo "--- make deploy with a host-built image"
start=$SECONDS
docker build -q --build-arg VERSION=hostbuild -t controller:latest . >/dev/null 2>"$T/docker-build.log" || { obs "1d: docker build failed"; tail -20 "$T/docker-build.log"; }
obs "1d: docker build of the team's Dockerfile took $((SECONDS - start))s"
kubectl kustomize config/default | kubectl apply -f - 2>&1 | tail -3
kubectl -n fleet-addons-system rollout status deploy/fleet-addons-controller-manager --timeout=90s >/dev/null 2>&1
obs "1d: with image controller:latest built on the host, the manager pod is: $(kubectl -n fleet-addons-system get pods -o jsonpath='{range .items[*]}{.status.phase}/{.status.containerStatuses[0].state.waiting.reason} {end}')"
kubectl -n fleet-addons-system get events --field-selector reason=Failed -o jsonpath='{range .items[*]}{.message}{"\n"}{end}' | head -2

echo "--- push to a public ephemeral registry instead"
IMG=ttl.sh/acme-fleet-addons-${GITHUB_RUN_ID:-local}-$RANDOM:2h
start=$SECONDS
docker tag controller:latest "$IMG" && docker push -q "$IMG" >/dev/null && obs "1d: pushed $IMG in $((SECONDS - start))s"
kubectl kustomize config/default | sed "s#image: controller:latest#image: $IMG#" | kubectl apply -f - >/dev/null
start=$SECONDS
if kubectl -n fleet-addons-system rollout status deploy/fleet-addons-controller-manager --timeout=240s; then
  obs "1d: manager from ttl.sh rolled out in $((SECONDS - start))s"
else
  obs "1d: manager from ttl.sh did not roll out: $(kubectl -n fleet-addons-system get pods -o wide 2>&1 | tail -2 | tr '\n' ' ')"
  kubectl -n fleet-addons-system describe pods | tail -20
fi
kubectl -n fleet-addons-system logs deploy/fleet-addons-controller-manager --tail=8 2>&1
obs "1d: manager log lines mentioning v1beta1 deprecation: $(kubectl -n fleet-addons-system logs deploy/fleet-addons-controller-manager 2>&1 | grep -ci 'deprecat')"

echo "--- webhook and samples"
for i in $(seq 1 20); do kubectl apply -n default -f config/samples/fleet_v1alpha1_fleetpolicy.yaml >"$T/sample.log" 2>&1 && break; sleep 6; done
obs "1d: valid FleetPolicy: $(cat "$T/sample.log" | tr '\n' ' ')"
obs "1d: invalid FleetPolicy: $(printf 'apiVersion: fleet.acme.io/v1alpha1\nkind: FleetPolicy\nmetadata:\n  name: bad\n  namespace: default\nspec:\n  message: ""\n' | kubectl create -f - 2>&1 | tr '\n' ' ')"

echo "--- Ginkgo e2e from the host"
start=$SECONDS
MGMT_KUBECONFIG=$KUBECONFIG WORKLOAD_KUBECONFIG=$W go test ./test/e2e/ -count=1 -v -ginkgo.v >"$T/e2e.log" 2>&1
obs "1d: e2e from host exit=$? in $((SECONDS - start))s: $(grep -E 'fleet-info version|Ran [0-9]+ of|FAIL!|SUCCESS!' "$T/e2e.log" | tr '\n' ' ')"
tail -25 "$T/e2e.log"

echo "--- the workload kubeconfig CAPI stores, as e2e frameworks read it"
kubectl get secret -n default work-kubeconfig -o jsonpath='{.data.value}' | base64 -d >"$T/work-secret.kubeconfig"
obs "1d: server in CAPI's work-kubeconfig secret: $(grep -m1 'server:' "$T/work-secret.kubeconfig" | tr -s ' ')"
obs "1d: kubectl with it from the host: $(kubectl --kubeconfig "$T/work-secret.kubeconfig" --request-timeout=10s get nodes 2>&1 | tail -1 | cut -c1-200)"
obs "1d: server in devenv's workload.kubeconfig: $(grep -m1 'server:' "$W" | tr -s ' ')"

echo "--- port-forward from the host"
kubectl -n fleet-addons-system port-forward svc/fleet-addons-webhook-service 19443:443 >"$T/pf.log" 2>&1 &
pf=$!
"$TOOL/explore/lib/pause.sh" 6
obs "1d: port-forward to the webhook service: HTTP $(curl -sk -o /dev/null -w '%{http_code}' -X POST https://127.0.0.1:19443/validate-fleet-acme-io-v1alpha1-fleetpolicy 2>&1)"
kill $pf 2>/dev/null

stop_up "$pid"
summary
