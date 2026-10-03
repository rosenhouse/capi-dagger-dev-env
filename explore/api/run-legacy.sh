#!/usr/bin/env bash
# A legacy controller module (controller-runtime v0.18, CAPI v1.8) adopting the tool through a nested module.
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
export GOWORK=off
L=$RUNNER_TEMP/oldctl
SUMMARY=()
obs() { echo "OBS: $*"; SUMMARY+=("$*"); }

cp -a "$here/legacy" "$L" && cd "$L" || exit 1
git init -q && git add -A && git -c user.email=x@example.com -c user.name=x commit -qm init
obs "legacy builds before adoption: $(go build -o /dev/null ./cmd/manager 2>&1 && echo ok)"

# What the README says: go get the tool into the controller's own module.
cp go.mod go.mod.orig; cp go.sum go.sum.orig
mkdir -p cmd/devenv && cp hack/devenv/main.go cmd/devenv/main.go
go mod edit -require=github.com/rosenhouse/capi-dagger-dev-env@v0.0.0-00010101000000-000000000000 -replace=github.com/rosenhouse/capi-dagger-dev-env="$repo"
go mod tidy > /dev/null 2>&1
obs "after adding the tool to the controller's module: $(grep -E '^go |k8s.io/client-go |controller-runtime ' go.mod | tr -s '\t ' ' ' | tr '\n' ';')"
obs "legacy manager builds after adoption: $(go build -o /dev/null ./cmd/manager 2>&1 | head -2 | tr '\n' ' ' | cut -c1-300)"
mv go.mod.orig go.mod; mv go.sum.orig go.sum; rm -rf cmd/devenv

# A nested module instead.
(cd hack/devenv && go mod edit -require=github.com/rosenhouse/capi-dagger-dev-env@v0.0.0-00010101000000-000000000000 -replace=github.com/rosenhouse/capi-dagger-dev-env="$repo" && go mod tidy > /dev/null 2>&1 && go build -o "$L/bin/devenv" .) || { echo "nested build failed"; exit 1; }
obs "legacy manager builds with the nested module: $(go build -o /dev/null ./cmd/manager 2>&1 && echo ok)"

t=$SECONDS
bin/devenv test --name lg > "$RUNNER_TEMP/legacy-test.log" 2>&1
obs "nested-module devenv test from module root: exit $? after $((SECONDS - t))s"
grep OBS-HOOK "$RUNNER_TEMP/legacy-test.log" | while read -r l; do obs "$l"; done
grep -vE '^\[' "$RUNNER_TEMP/legacy-test.log" | tail -6

# The same binary run from its own directory, with Root pointing up to the controller's module.
cd hack/devenv
t=$SECONDS
pid=$("$repo/explore/lib/up-bg.sh" "$RUNNER_TEMP/legacy-root.log" 1200 -- env DEVENV_ROOT=../.. "$L/bin/devenv" up --name lr)
obs "Root=../.. from hack/devenv: pid '$pid' after $((SECONDS - t))s; state dirs: $(find "$L" -name .devenv -type d | sed "s#$L/##" | tr '\n' ' ')"
if [ -n "$pid" ]; then
  export KUBECONFIG=$L/hack/devenv/.devenv/lr/mgmt.kubeconfig
  obs "legacy manager pod: $(kubectl get pods -n oldctl --no-headers 2>&1 | tr '\n' '|')"
  obs "legacy manager log: $(kubectl logs -n oldctl deploy/manager --tail=5 2>&1 | cut -c1-200 | tr '\n' '|')"
  obs "clusters CRD versions: $(kubectl get crd clusters.cluster.x-k8s.io -o jsonpath='{range .spec.versions[*]}{.name} served={.served} storage={.storage}; {end}' 2>&1)"
  "$L/bin/devenv" down --name lr 2>&1 | tail -1
  while kill -0 "$pid" 2>/dev/null; do sleep 2; done
else
  tail -20 "$RUNNER_TEMP/legacy-root.log"
fi
echo "===== SUMMARY ====="
printf '%s\n' "${SUMMARY[@]}"
