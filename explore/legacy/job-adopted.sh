#!/usr/bin/env bash
# Steps 1-3 with the workarounds a team would find: a nested devenv module, vendor/, kustomize
# output rendered for the tool's image names, e2e from the host, redeploys, then devenv test.
source "$(dirname "$0")/lib.sh"
T=$RUNNER_TEMP
C=$T/adopted
copy_repo "$C"; devenv_module "$C"; vendor_repo "$C/repo"
render "$C/repo" config/devenv deploy/devenv/rendered.yaml
cd "$C/repo/hack/devenv" || exit 1
export DEVENV_SCENARIO=rendered
start=$SECONDS
if ! pid=$("$TOOL/explore/lib/up-bg.sh" "$T/up.log" 1800 -- "$C/devenv" up --name adopted); then
  obs "rendered up failed after $((SECONDS - start))s: $(grep -m1 '^Error' "$T/up.log" | cut -c1-300)"
  state_tail "$C/repo/hack/devenv" adopted
  summary; exit 1
fi
obs "rendered up took $((SECONDS - start))s"
grep '^\[' "$T/up.log" | tail -30
grep 'OBS: Images hook' "$T/up.log"
D=$C/repo/hack/devenv/.devenv/adopted
export KUBECONFIG=$D/mgmt.kubeconfig
W=$D/workload.kubeconfig
obs "git status after up: $(git -C "$C/repo" status --porcelain | head -5 | tr '\n' ' ')"
kubectl get pkgi,apps -A
obs "manager image: $(kubectl -n fleet-addons-system get deploy fleet-addons-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image} env={.spec.template.spec.containers[0].env}')"

fleet_info() { kubectl --kubeconfig "$W" -n fleet-system get cm fleet-info -o jsonpath='{.data.version}' 2>/dev/null; }
for i in $(seq 1 36); do [ -n "$(fleet_info)" ] && break; sleep 5; done
obs "fleet-info version in workload cluster: '$(fleet_info)'"
for i in $(seq 1 24); do kubectl --kubeconfig "$W" -n fleet-system rollout status deploy/fleet-agent --timeout=5s >/dev/null 2>&1 && break; sleep 5; done
obs "agent in workload: $(kubectl --kubeconfig "$W" -n fleet-system get deploy fleet-agent -o jsonpath='{.spec.template.spec.containers[0].image} ready={.status.readyReplicas}' 2>&1)"
obs "agent log: $(kubectl --kubeconfig "$W" -n fleet-system logs deploy/fleet-agent --tail=1 2>&1)"
obs "manager log deprecation lines: $(kubectl -n fleet-addons-system logs deploy/fleet-addons-controller-manager 2>&1 | grep -ci deprecat)"

echo "--- e2e from the host"
(cd "$C/repo" && MGMT_KUBECONFIG=$KUBECONFIG WORKLOAD_KUBECONFIG=$W go test ./test/e2e/ -count=1 -v -ginkgo.v >"$T/e2e1.log" 2>&1)
obs "e2e from host exit=$?: $(grep -E 'fleet-info version|Ran [0-9]+ of|FAIL!|SUCCESS!' "$T/e2e1.log" | tr '\n' ' ')"

bundles() { kubectl get apps -A -o jsonpath='{range .items[*]}{.spec.fetch[0].imgpkgBundle.image}{"\n"}{end}' | grep fleet; }
pods() { kubectl -n fleet-addons-system get pods -o jsonpath='{.items[*].metadata.name}'; }

echo "--- redeploy with no change"
b0=$(bundles); p0=$(pods)
start=$SECONDS
"$C/devenv" redeploy --name adopted 2>&1 | tail -5
obs "no-op redeploy #1 took $((SECONDS - start))s; bundle changed: $([ "$b0" != "$(bundles)" ] && echo yes || echo no); manager pod replaced: $([ "$p0" != "$(pods)" ] && echo yes || echo no)"
b1=$(bundles)
start=$SECONDS
"$C/devenv" redeploy --name adopted 2>&1 | tail -3
obs "no-op redeploy #2 took $((SECONDS - start))s; bundle changed: $([ "$b1" != "$(bundles)" ] && echo yes || echo no)"
grep 'OBS: Images hook' "$T/up.log" | tail -3

echo "--- redeploy --version after a code change"
sed -i 's/Version = "unknown"/Version = "edited"/' "$C/repo/internal/version/version.go"
start=$SECONDS
"$C/devenv" redeploy --name adopted --version v2 2>&1 | tail -3
obs "redeploy --version v2 after a code change took $((SECONDS - start))s"
for i in $(seq 1 24); do [ "$(fleet_info)" != "unknown" ] && break; sleep 5; done
obs "after redeploy --version v2: fleet-info version='$(fleet_info)'; agent log: $(kubectl --kubeconfig "$W" -n fleet-system logs deploy/fleet-agent --tail=1 2>&1)"
(cd "$C/repo" && WANT_VERSION=v2 MGMT_KUBECONFIG=$KUBECONFIG WORKLOAD_KUBECONFIG=$W go test ./test/e2e/ -count=1 >"$T/e2e2.log" 2>&1)
obs "e2e expecting version v2 exit=$?: $(grep -m1 -E 'Expected|SUCCESS!' "$T/e2e2.log")"

echo "--- an edit to kustomize source, then redeploy without re-rendering"
sed -i 's/memory: 128Mi/memory: 256Mi/' "$C/repo/config/manager/manager.yaml"
"$C/devenv" redeploy --name adopted 2>&1 | tail -2
obs "after editing config/manager and redeploying, manager memory limit: $(kubectl -n fleet-addons-system get deploy fleet-addons-controller-manager -o jsonpath='{.spec.template.spec.containers[0].resources.limits.memory}')"

stop_up "$pid"

echo "--- devenv test with the Ginkgo Test hook"
git -C "$C/repo" checkout -q -- .
start=$SECONDS
timeout 1500 "$C/devenv" test --name adopted >"$T/test.log" 2>&1
rc=$?
obs "devenv test exit=$rc in $((SECONDS - start))s: $(grep -E 'passed|^Error' "$T/test.log" | head -2 | tr '\n' ' ')"
grep -A32 'OBS: Test hook' "$T/test.log" | tail -34
[ $rc -ne 0 ] && state_tail "$C/repo/hack/devenv" adopted
summary
