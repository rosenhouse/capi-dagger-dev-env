#!/usr/bin/env bash
# Corporate proxy: containers on the Docker bridge may reach the internet only through squid.
# The engine is provisioned with HTTP_PROXY and HTTPS_PROXY, as Dagger documents.
# Usage: proxy.sh NO_PROXY_VALUE
source "$(dirname "$0")/lib.sh"
NOPX=${1:-localhost,127.0.0.1}
G=$REPO/examples/greeting
D=$T/devenv
cd "$G" && go build -o "$D" ./cmd/devenv || exit 1
start_squid
obs "direct from bridge: $(docker run --rm curlimages/curl:8.11.1 -sS -m 10 -o /dev/null -w '%{http_code}' https://registry-1.docker.io/v2/ 2>&1 | tail -1)"
obs "via squid from bridge: $(docker run --rm curlimages/curl:8.11.1 -sS -m 10 -x $PX -o /dev/null -w '%{http_code}' https://registry-1.docker.io/v2/ 2>&1 | tail -1)"

docker run -d --name dagger-engine-proxy --privileged \
  -e HTTPS_PROXY=$PX -e https_proxy=$PX -e HTTP_PROXY=$PX -e http_proxy=$PX -e NO_PROXY=$NOPX -e no_proxy=$NOPX \
  -v dagger-proxy:/var/lib/dagger registry.dagger.io/engine:v0.21.10 >/dev/null
export _EXPERIMENTAL_DAGGER_RUNNER_HOST=docker-container://dagger-engine-proxy
run informed timeout 1800 "$D" test --name px
obs "informed (NO_PROXY=$NOPX): stages: $(grep -o '] [a-z].*' "$T/informed.err" | cut -c3-40 | tr '\n' '|')"
L=$G/.devenv/px
echo "--- dagger.log tail"; tail -15 "$L/dagger.log"
if [ -d "$L/logs" ]; then
  echo "--- image pull and proxy errors in exported logs"
  grep -rhoE '.{0,160}(proxy|dagger\.local|ErrImagePull|ImagePullBackOff|failed to pull|failed to resolve|x509|no such host|Forbidden).{0,160}' "$L/logs" 2>/dev/null | grep -v front-proxy | sort | uniq -c | sort -rn | head -25
  grep -B2 -A3 usefulErrorMessage "$L/logs/resources.yaml" 2>/dev/null | head -40
fi
squid_summary
finish
