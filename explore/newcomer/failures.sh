#!/usr/bin/env bash
# Redeploying a crash-looping controller, then the fix; which controllers restart for a one-command edit.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
ensure_kubectl
cd "$here/../../examples/greeting"
go build -o /tmp/devenv ./cmd/devenv
D() { /tmp/devenv "$@"; }

start_up "$RUNNER_TEMP/f.log" /tmp/devenv up --name f
wait_up "$RUNNER_TEMP/f.log" 1200 || { dump_state .devenv/f; summary; exit 1; }
M="kubectl --kubeconfig .devenv/f/mgmt.kubeconfig"
W="kubectl --kubeconfig .devenv/f/workload.kubeconfig"

echo "################ which pods restart when only hello changes"
pods() { { $M get pods -n addon-manager -o name; $M get pods -n greeting-syncer -o name; $W get pods -n greeting-controller -o name; } | sort | tr '\n' ' '; }
before=$(pods); obs "pods before: $before"
sed -i 's|"%s (hello %s)\\n"|"%s [edited hello %s]\\n"|' cmd/hello/main.go
timed "redeploy after editing only hello" D redeploy
$M -n addon-manager rollout status deploy/addon-manager --timeout=60s >/dev/null
$M -n greeting-syncer rollout status deploy/greeting-syncer --timeout=60s >/dev/null
$W -n greeting-controller rollout status deploy/greeting-controller --timeout=60s >/dev/null
after=$(pods); obs "pods after: $after"
note "controllers replaced by a hello-only edit: $(comm -13 <(tr ' ' '\n' <<<"$before" | sort) <(tr ' ' '\n' <<<"$after" | sort) | grep -c pod/) of $(tr ' ' '\n' <<<"$before" | grep -c pod/)"
$M get pods -A -l app.kubernetes.io/name -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,IMAGE:.spec.containers[0].image | grep -v -E 'capi|cert|kapp' || true

echo "################ redeploy a controller that crashes on start"
cp cmd/greeting-syncer/main.go "$RUNNER_TEMP/gs.go"
sed -i '0,/^func main() {/s//func main() {\n\tpanic("crash on start")/' cmd/greeting-syncer/main.go
timed_tail "redeploy with crash-looping greeting-syncer" 30 D redeploy
$M -n greeting-syncer get pods
$M -n devenv get apps
$M -n devenv get app greeting-syncer -o jsonpath='{.status.friendlyDescription}{"\n"}{.status.usefulErrorMessage}{"\n"}' | tail -15
echo "################ fix the crash and redeploy"
cp "$RUNNER_TEMP/gs.go" cmd/greeting-syncer/main.go
timed_tail "redeploy after fixing the crash" 30 D redeploy
$M -n greeting-syncer get pods
$M -n devenv get apps
timed_tail "second redeploy after fixing the crash" 30 D redeploy
$M -n devenv get apps
$M -n greeting-syncer get pods

echo "################ up's own output"
sed -n '/is up\./,$p' "$RUNNER_TEMP/f.log"
timed "down --purge" D down --name f --purge
summary
