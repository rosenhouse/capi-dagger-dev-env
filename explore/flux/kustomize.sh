#!/usr/bin/env bash
# A kubebuilder-style kustomize tree as a package's config; then Flux installed by the Ready hook and driven by the Test hook.
source "$(dirname "$0")/lib.sh"
setup_consumer
cd "$W" || exit 1

echo "=== Package config that is a kustomize tree"
t=$SECONDS
FLUXCO_SCENARIO=kustomize "$D" up --name kz > "$RUNNER_TEMP/up-kz.log" 2>&1 &
pid=$!
while kill -0 $pid 2>/dev/null && ! grep -q " is up\." "$RUNNER_TEMP/up-kz.log"; do sleep 5; done
if grep -q " is up\." "$RUNNER_TEMP/up-kz.log"; then
  obs "kustomize-tree package: up SUCCEEDED after $(since $t)s"
  export MGMT=$W/.devenv/kz/mgmt.kubeconfig
  k get apps -A
  k get deploy -A | grep -i manager
  k get kustomizations.kustomize.config.k8s.io -A 2>&1 | head -3
  "$D" down --name kz
  wait $pid
else
  obs "kustomize-tree package: up failed after $(since $t)s"
  grep -E '^\[|Error|error' "$RUNNER_TEMP/up-kz.log" | tail -25
  dump kz
fi
"$D" down --name kz --purge >/dev/null 2>&1

echo "=== devenv test: Ready hook installs Flux, Test hook deploys with it and redeploys"
export FLUXCO_SCENARIO=refs FLUXCO_READY=$TOOL/explore/flux/ready-flux.sh FLUXCO_TEST=$TOOL/explore/flux/test-flux.sh D TOOL
t=$SECONDS
"$D" test --name rt 2>&1 | tee "$RUNNER_TEMP/test-rt.log" | grep -vE '^\s*$' | tail -80
obs "devenv test with Flux hooks: exit=${PIPESTATUS[0]} after $(since $t)s"
grep -h '^OBS:' "$RUNNER_TEMP/test-rt.log" | sed 's/^OBS: /  hook: /'
grep -E '^\[' "$RUNNER_TEMP/test-rt.log"
summary
