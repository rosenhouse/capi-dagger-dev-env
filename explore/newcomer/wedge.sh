#!/usr/bin/env bash
# Usage: wedge.sh COMPONENT. Redeploys COMPONENT crashing on start, then the fix, and records what kapp-controller sees.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
ensure_kubectl
comp=$1
cd "$here/../../examples/greeting"
go build -o /tmp/devenv ./cmd/devenv
D() { /tmp/devenv "$@"; }
start_up "$RUNNER_TEMP/w.log" /tmp/devenv up --name w
wait_up "$RUNNER_TEMP/w.log" 1200 || { dump_state .devenv/w; summary; exit 1; }
M="kubectl --kubeconfig .devenv/w/mgmt.kubeconfig"
W="kubectl --kubeconfig .devenv/w/workload.kubeconfig"
K=$M; [ "$comp" = greeting-controller ] && K=$W
snapshot() {
  echo "--- snapshot: $1"
  $M get apps -A -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,GEN:.metadata.generation,OBSERVED:.status.observedGeneration,DESC:.status.friendlyDescription,FETCH:.spec.fetch[0].imgpkgBundle.image' | sed 's/sha256:\(.......\)[0-9a-f]*/\1/'
  $M get packages -A -o custom-columns='NAME:.metadata.name,BUNDLE:.spec.template.spec.fetch[0].imgpkgBundle.image' | sed 's/sha256:\(.......\)[0-9a-f]*/\1/'
  $M get packageinstalls -A -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,GEN:.metadata.generation,OBSERVED:.status.observedGeneration,DESC:.status.friendlyDescription'
  $K -n "$comp" get deploy,rs,pods -o wide | cut -c1-160
}
snapshot "after up"
echo "################ redeploy $comp crashing on start"
cp "cmd/$comp/main.go" "$RUNNER_TEMP/main.go"
sed -i '0,/^func main() {/s//func main() {\n\tpanic("crash on start")/' "cmd/$comp/main.go"
timed_tail "redeploy with crash-looping $comp" 20 D redeploy
snapshot "after crash redeploy"
$K -n "$comp" get deploy "$comp" -o jsonpath='{.spec.strategy}{"\n"}{.status}{"\n"}'
echo "################ fix and redeploy"
cp "$RUNNER_TEMP/main.go" "cmd/$comp/main.go"
timed_tail "redeploy after fixing $comp" 20 D redeploy
snapshot "after fix redeploy"
$M -n kapp-controller logs deploy/kapp-controller --tail=40 2>&1 | cut -c1-300
for i in 1 2 3 4 5; do
  sleep 60
  obs "minute $i after the fix redeploy: $($M get apps -A -o custom-columns='N:.metadata.name,D:.status.friendlyDescription' --no-headers | tr -s ' ' | tr '\n' ';') pods: $($K -n "$comp" get pods --no-headers | awk '{print $3}' | tr '\n' ' ')"
done
snapshot "five minutes later"
timed_tail "redeploy again, five minutes later" 20 D redeploy
snapshot "end"
D down --name w --purge
summary
