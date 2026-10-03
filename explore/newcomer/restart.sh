#!/usr/bin/env bash
# Commands during bring-up, Ctrl-C mid-bring-up, restarts of one name, kubeconfig stability, and a killed up.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
ensure_kubectl
cd "$here/../../examples/greeting"
go build -o /tmp/devenv ./cmd/devenv
D() { /tmp/devenv "$@"; }

echo "################ commands while up is coming up, then Ctrl-C mid-way"
start_up "$RUNNER_TEMP/early.log" /tmp/devenv up --name early
last=""
for i in $(seq 200); do
  stage=$(grep '^\[' "$RUNNER_TEMP/early.log" | tail -1)
  if [ "$stage" != "$last" ]; then
    last=$stage
    obs "stage: $stage"
    obs "  status: $(D status 2>&1 | tail -1)"
    obs "  kubeconfig: $(D kubeconfig --name early 2>&1 | head -c 120 | tr '\n' ' ')"
    obs "  redeploy: $(timeout 20 /tmp/devenv redeploy --name early 2>&1 | tail -1)"
    obs "  second up: $(timeout 20 /tmp/devenv up --name early 2>&1 | tail -1)"
  fi
  case $stage in *"management packages"*|*"workload cluster"*) break;; esac
  kill -0 "$UP_PID" 2>/dev/null || break
  sleep 3
done
note "sending Ctrl-C during stage: $last"
ctrl_c
echo "--- early up output"; cat "$RUNNER_TEMP/early.log"
D status

echo "################ up --name stable, cold for this name"
start_up "$RUNNER_TEMP/stable1.log" /tmp/devenv up --name stable
wait_up "$RUNNER_TEMP/stable1.log" 1500 || { dump_state .devenv/stable; summary; exit 1; }
cat "$RUNNER_TEMP/stable1.log"
cp .devenv/stable/mgmt.kubeconfig "$RUNNER_TEMP/mgmt1"; cp .devenv/stable/workload.kubeconfig "$RUNNER_TEMP/workload1"
mkdir -p ~/.kube
KUBECONFIG=.devenv/stable/mgmt.kubeconfig:.devenv/stable/workload.kubeconfig kubectl config view --flatten > ~/.kube/config
kubectl config get-contexts
kubectl --context stable-mgmt get nodes; note "merged stable-mgmt works: exit $?"
note "listening sockets for devenv: $(ss -ltnp 2>/dev/null | grep -c devenv) ($(ss -ltnp 2>/dev/null | grep devenv | awk '{print $4}' | tr '\n' ' '))"
ss -ltnp | grep -E 'dagger|devenv' || true

echo "################ Ctrl-C a running environment"
ctrl_c
sed -n '/Press Ctrl-C/,$p' "$RUNNER_TEMP/stable1.log"
D status
docker ps --format '{{.Names}} {{.Status}}'
pgrep -af 'session --label dagger.io' || true

echo "################ up --name stable again (warm)"
start_up "$RUNNER_TEMP/stable2.log" /tmp/devenv up --name stable
wait_up "$RUNNER_TEMP/stable2.log" 1500 || { dump_state .devenv/stable; summary; exit 1; }
cat "$RUNNER_TEMP/stable2.log"
for c in mgmt workload; do
  if cmp -s "$RUNNER_TEMP/${c}1" ".devenv/stable/$c.kubeconfig"; then note "$c kubeconfig identical across restart"; else
    note "$c kubeconfig changed across restart: server $(grep server: "$RUNNER_TEMP/${c}1" | awk '{print $2}') -> $(grep server: .devenv/stable/$c.kubeconfig | awk '{print $2}'); CA same: $(cmp -s <(grep certificate-authority-data "$RUNNER_TEMP/${c}1") <(grep certificate-authority-data .devenv/stable/$c.kubeconfig) && echo yes || echo no)"
  fi
done
kubectl --context stable-mgmt get nodes --request-timeout=10s >"$RUNNER_TEMP/k.out" 2>&1; note "previously merged stable-mgmt context after restart: exit $?: $(tail -1 "$RUNNER_TEMP/k.out")"

echo "################ SIGHUP up's process group, as closing its terminal would"
pgrep -af 'session --label dagger.io|devenv up' || true
kill -HUP -- "-$UP_PID"; s=$SECONDS
wait "$UP_PID" 2>/dev/null; note "up exited $? after SIGHUP"
D status
note "after SIGHUP: status says '$(D status | tail -1)'"
pgrep -af 'session --label dagger.io' && note "dagger session processes survive SIGHUP of up's process group" || note "no dagger session processes after SIGHUP"
kubectl --kubeconfig .devenv/stable/mgmt.kubeconfig get nodes --request-timeout=10s >"$RUNNER_TEMP/k.out" 2>&1; note "kubectl after SIGHUP: exit $?: $(tail -1 "$RUNNER_TEMP/k.out")"
ls -la ~/.cache/devenv/ 2>&1
start_up "$RUNNER_TEMP/stable3.log" /tmp/devenv up --name stable
wait_up "$RUNNER_TEMP/stable3.log" 900 || { dump_state .devenv/stable; }
cat "$RUNNER_TEMP/stable3.log"

echo "################ kill -9 only the devenv process (not its dagger child)"
pgrep -af 'session --label dagger.io' || true
kill -9 "$UP_PID"
wait "$UP_PID" 2>/dev/null
note "after kill -9 of devenv alone: status '$(D status | tail -1)'; dagger session procs: $(pgrep -f 'session --label dagger.io' | wc -l)"
start_up "$RUNNER_TEMP/stable4.log" /tmp/devenv up --name stable
wait_up "$RUNNER_TEMP/stable4.log" 600 || { dump_state .devenv/stable; }
cat "$RUNNER_TEMP/stable4.log"
timed "down stable" D down --name stable
pgrep -af 'session --label dagger.io' || true
pkill -9 -f 'session --label dagger.io' 2>/dev/null
timed "down --purge stable" D down --name stable --purge
timed "down --purge early" D down --name early --purge
D status
summary
