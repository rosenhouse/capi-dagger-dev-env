#!/usr/bin/env bash
# A team's first attempts: the README path in the controller's own module, then the kubebuilder
# config tree as a package.
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
  expect_fail "$T/same-up.log" 420 -- "$A/devenv" up --name same
  state_tail "$A/repo" same
fi

echo "### 1c: the kubebuilder config tree as the package config"
B=$T/raw
copy_repo "$B"; devenv_module "$B"; vendor_repo "$B/repo"
cd "$B/repo/hack/devenv" || exit 1
export DEVENV_SCENARIO=raw-config
expect_fail "$T/raw-up.log" 900 -- "$B/devenv" up --name raw
state_tail "$B/repo/hack/devenv" raw
summary
