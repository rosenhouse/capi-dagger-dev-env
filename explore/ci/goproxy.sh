#!/usr/bin/env bash
# A corporate network that blocks proxy.golang.org, where developers set GOPROXY to an internal module proxy.
# goproxy.io stands in for the internal proxy.
source "$(dirname "$0")/lib.sh"
G=$REPO/examples/greeting
D=$T/devenv
cd "$G" && go build -o "$D" ./cmd/devenv || exit 1
start_squid proxy.golang.org
export GOPROXY=https://goproxy.io GONOSUMDB=example.internal
obs "host: GOPROXY=$GOPROXY; host go build of the example: $(GOFLAGS=-modcacherw GOMODCACHE=$T/hostmod go build -o /dev/null ./cmd/... 2>&1 | tail -1; echo rc=$?)"

engine_with() { # extra docker run args
  docker rm -f dagger-engine-gp >/dev/null 2>&1
  docker run -d --name dagger-engine-gp --privileged -e HTTPS_PROXY=$PX -e https_proxy=$PX "$@" \
    -v dagger-gp:/var/lib/dagger registry.dagger.io/engine:v0.21.10 >/dev/null
}
export _EXPERIMENTAL_DAGGER_RUNNER_HOST=docker-container://dagger-engine-gp

engine_with
run hostgoproxy timeout 1200 "$D" test --name gp
obs "hostgoproxy: stages: $(grep -o '] [a-z].*' "$T/hostgoproxy.err" | cut -c3-40 | tr '\n' '|')"
grep -n -i -E 'proxy.golang.org|goproxy|Forbidden|403|go: ' "$T/hostgoproxy.err" | head -15
squid_summary

engine_with -e _DAGGER_ENGINE_SYSTEMENV_GOPROXY=https://goproxy.io
run enginegoproxy timeout 1500 "$D" test --name gp
obs "enginegoproxy: stages: $(grep -o '] [a-z].*' "$T/enginegoproxy.err" | cut -c3-40 | tr '\n' '|')"
grep -n -i -E 'proxy.golang.org|goproxy|Forbidden|403|go: ' "$T/enginegoproxy.err" | head -15
squid_summary
finish
