#!/usr/bin/env bash
# Behind the proxy, with a warm engine, one upstream registry becomes unreachable.
source "$(dirname "$0")/lib.sh"
G=$REPO/examples/greeting
D=$T/devenv
cd "$G" && go build -o "$D" ./cmd/devenv || exit 1
start_squid
NOPX=localhost,127.0.0.1,docker,.dagger.local
docker run -d --name dagger-engine-proxy --privileged \
  -e HTTPS_PROXY=$PX -e https_proxy=$PX -e HTTP_PROXY=$PX -e http_proxy=$PX -e NO_PROXY=$NOPX -e no_proxy=$NOPX \
  -v dagger-proxy:/var/lib/dagger registry.dagger.io/engine:v0.21.10 >/dev/null
export _EXPERIMENTAL_DAGGER_RUNNER_HOST=docker-container://dagger-engine-proxy
run warmup timeout 1200 "$D" test --name w
squid_summary

restart_squid_denying() {
  docker rm -f squid >/dev/null
  sudo iptables -D DOCKER-USER -i docker0 ! -o docker0 -p tcp -m multiport --dports 80,443 -j REJECT --reject-with tcp-reset
  start_squid "$@"
}
restart_squid_denying .quay.io
run quay-down timeout 900 "$D" test --name w
obs "quay-down: stages: $(grep -o '] [a-z].*' "$T/quay-down.err" | cut -c3-40 | tr '\n' '|')"
squid_summary
restart_squid_denying .docker.io .docker.com
run dockerhub-blocked timeout 900 "$D" test --name w
obs "dockerhub-blocked: stages: $(grep -o '] [a-z].*' "$T/dockerhub-blocked.err" | cut -c3-40 | tr '\n' '|')"
squid_summary
finish
