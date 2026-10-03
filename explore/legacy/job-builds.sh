#!/usr/bin/env bash
# Build-side variants: the team's Dockerfile and controller:latest placeholder through devenv test,
# then layouts and module setups that the tool's own build meets in legacy repos.
source "$(dirname "$0")/lib.sh"
T=$RUNNER_TEMP
X=$T/base
copy_repo "$X"; devenv_module "$X"; vendor_repo "$X/repo"
render "$X/repo" config/devenv deploy/devenv/rendered.yaml
render "$X/repo" config/default deploy/legacy/rendered.yaml
variant() { rm -rf "$T/$1"; cp -a "$X" "$T/$1"; echo "$T/$1"; }

echo "### 3f: Dockerfile build, controller:latest placeholder, make-deploy manifests, Ginkgo Test hook"
V=$(variant dockerfile)
cd "$V/repo/hack/devenv" || exit 1
start=$SECONDS
DEVENV_SCENARIO=dockerfile timeout 2000 "$V/devenv" test --name df >"$T/df.log" 2>&1
rc=$?
obs "3f: devenv test exit=$rc in $((SECONDS - start))s: $(grep -E 'passed|^Error' "$T/df.log" | head -2 | cut -c1-300 | tr '\n' ' ')"
grep '^\[' "$T/df.log" | tail -25
grep -A32 'OBS: Test hook' "$T/df.log" | tail -34
[ $rc -ne 0 ] && state_tail "$V/repo/hack/devenv" df

echo "### 3a: kubebuilder v3 layout, main.go at the module root"
V=$(variant rootmain)
(cd "$V/repo" && git mv cmd/main.go main.go && git commit -qm v3)
cd "$V/repo/hack/devenv" || exit 1
DEVENV_SCENARIO=root-main-dot "$V/devenv" up --name b >"$T/rootdot.log" 2>&1
obs "3a: Commands [\".\"]: $(grep -m1 '^Error' "$T/rootdot.log")"
DEVENV_SCENARIO=root-main expect_fail "$T/rootmain.log" 900 -- "$V/devenv" up --name b

echo "### 3b: a private module the host can fetch, without vendor/"
V=$(variant private)
cd "$V/repo" || exit 1
git rm -rq vendor
mkdir -p third_party && cp -a ../common third_party/fleet-common
go mod edit -dropreplace=github.com/acme/fleet-common -replace=github.com/acme/fleet-common=./third_party/fleet-common
P=$T/goproxy; mod=git.internal.acme.example/platform/lib; ver=v1.2.0
mkdir -p "$P/$mod/@v" "$T/zipsrc/$mod@$ver"
printf 'module %s\n\ngo 1.22\n' "$mod" >"$T/zipsrc/$mod@$ver/go.mod"
printf 'package lib\n\nconst Name = "acme"\n' >"$T/zipsrc/$mod@$ver/lib.go"
(cd "$T/zipsrc" && zip -qr "$P/$mod/@v/$ver.zip" "$mod@$ver")
cp "$T/zipsrc/$mod@$ver/go.mod" "$P/$mod/@v/$ver.mod"
echo '{"Version":"v1.2.0","Time":"2024-01-01T00:00:00Z"}' >"$P/$mod/@v/$ver.info"
echo "$ver" >"$P/$mod/@v/list"
printf 'package version\n\nimport _ "%s"\n' "$mod" >internal/version/private.go
export GOPROXY=file://$P,https://proxy.golang.org GONOSUMDB=git.internal.acme.example
go get "$mod@$ver" && go build ./... && obs "3b: host builds with the private module through its GOPROXY"
unset GOPROXY GONOSUMDB
git add -A && git commit -qm private
cd hack/devenv || exit 1
GOPROXY=file://$P,https://proxy.golang.org GONOSUMDB=git.internal.acme.example DEVENV_SCENARIO=build-only \
  expect_fail "$T/private.log" 900 -- "$V/devenv" up --name b

echo "### 3c: go.mod asks for a newer Go patch release than the tool's builder"
V=$(variant gonewer)
(cd "$V/repo" && go mod edit -go=1.26.8 && git commit -qam go1268)
cd "$V/repo/hack/devenv" || exit 1
DEVENV_SCENARIO=build-only expect_fail "$T/gonewer.log" 900 -- "$V/devenv" up --name b

echo "### 3d: go:generate output that .gitignore lists"
V=$(variant generated)
cd "$V/repo" || exit 1
mkdir -p internal/assets
cat >internal/assets/assets.go <<'EOF'
// Package assets embeds the CRDs, which go generate copies here.
package assets

//go:generate sh -c "mkdir -p crds && cp ../../config/crd/bases/*.yaml crds/"

import "embed"

//go:embed crds/*.yaml
var CRDs embed.FS
EOF
printf 'package main\n\nimport _ "github.com/acme/fleet-addons/internal/assets"\n' >cmd/assets.go
echo 'internal/assets/crds/' >>.gitignore
go generate ./internal/assets/ && go build ./cmd/ && obs "3d: host builds after go generate"
git add -A && git commit -qm generated
cd hack/devenv || exit 1
DEVENV_SCENARIO=build-only expect_fail "$T/generated.log" 900 -- "$V/devenv" up --name b

summary
