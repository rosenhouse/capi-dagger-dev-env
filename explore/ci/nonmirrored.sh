#!/usr/bin/env bash
# Behind the proxy (with the NO_PROXY workaround), can each cluster pull an image from a registry devenv does not mirror?
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
(while sleep 5; do free -m | awk '/Mem/{print $3}'; done >"$T/mem.log") &
sampler=$!
pid=$("$REPO/explore/lib/up-bg.sh" "$T/up.log" 1200 -- "$D" up --name nm) || { obs "up failed"; tail -30 "$T/up.log"; finish; exit 0; }
squid_summary
IMG=mcr.microsoft.com/oss/kubernetes/pause:3.6
for c in mgmt workload; do
  k="kubectl --kubeconfig .devenv/nm/$c.kubeconfig"
  $k run nonmirrored --image=$IMG --restart=Never >/dev/null
  $k wait --for=condition=Ready pod/nonmirrored --timeout=120s >/dev/null 2>&1
  obs "$c: pod from $IMG: $($k get pod nonmirrored -o jsonpath='{.status.phase} {.status.containerStatuses[0].state.waiting.reason}')"
  $k get events --field-selector involvedObject.name=nonmirrored -o custom-columns=MSG:.message --no-headers | tail -3
done
squid_summary
obs "memory: host used MB peak $(sort -n "$T/mem.log" | tail -1) of $(free -m | awk '/Mem/{print $2}'); engine now $(docker stats --no-stream --format '{{.MemUsage}}' dagger-engine-proxy)"
obs "cpu load average: $(cut -d' ' -f1-3 /proc/loadavg)"
kill $sampler
"$D" down --name nm >/dev/null 2>&1
wait "$pid"
finish
