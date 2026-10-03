#!/usr/bin/env bash
# Repeat the sequential six-cluster scenario that lost the management API in runs 1 and 3, from a Test hook,
# with host-side diagnostics when the management API stops answering.
source "$(dirname "$0")/lib.sh"
install_tools
src=$RUNNER_TEMP/consumer
cp -a "$GREETING" "$src"
rm -rf "$src/.devenv"
cp "$REPO/explore/multicluster/consumer/capseq.go.txt" "$src/cmd/devenv/capseq.go"
sed -i 's/Test:  greeting,/Test:  capacitySequential,/' "$src/cmd/devenv/main.go"
cd "$src" || exit 1
export GOWORK=off
go mod edit -droprequire=github.com/rosenhouse/capi-dagger-dev-env
go get github.com/rosenhouse/capi-dagger-dev-env@2eacde6 >/dev/null 2>&1
go mod tidy
go build -o "$BIN/mdevenv" ./cmd/devenv || { obs "consumer build failed"; summary; exit 1; }
dir=$src/.devenv/cs

diagnose() {
  echo "=== diagnostics at $(date +%H:%M:%S)"
  sudo ss -tlnp | grep -E 'devenv|dagger' | tee "$RUNNER_TEMP/ss.txt"
  for port in $(grep -oE '(127\.0\.0\.1|0\.0\.0\.0|\*|\[::\]):[0-9]+' "$RUNNER_TEMP/ss.txt" | sed -E 's/.*://' | sort -u); do
    proc=$(grep -E ":$port " "$RUNNER_TEMP/ss.txt" | grep -oE 'users:\(\("[^"]+' | sed 's/users:(("//')
    echo "port $port ($proc): $(curl -sk --max-time 5 "https://127.0.0.1:$port/readyz" 2>&1 | head -c 80; echo " rc=$?")"
  done
  echo "mgmt kubeconfig server: $(grep server: "$dir/mgmt.kubeconfig")  workload: $(grep server: "$dir/workload.kubeconfig")"
  echo "workload API via kubeconfig: $(kubectl --kubeconfig "$dir/workload.kubeconfig" --request-timeout=5s get --raw /readyz 2>&1 | tail -1)"
  docker stats --no-stream --format '{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}'
  top -bn1 | head -15
}
watcher() {
  until [ -f "$dir/mgmt.kubeconfig" ] && [ -f "$dir/workload.kubeconfig" ]; do sleep 5; done
  local fails=0 diagnosed=0
  while true; do
    if kubectl --kubeconfig "$dir/mgmt.kubeconfig" --request-timeout=5s get --raw /readyz >/dev/null 2>&1; then
      [ $fails -ge 5 ] && echo "$(date +%H:%M:%S) mgmt API answering again after $fails failures"
      fails=0
    else
      fails=$((fails + 1))
      [ $fails = 1 ] && echo "$(date +%H:%M:%S) mgmt API probe failed"
      if [ $fails -ge 12 ] && [ $diagnosed = 0 ]; then diagnose; diagnosed=1; fi
    fi
    echo "$(date +%H:%M:%S) load=$(cut -d' ' -f1 /proc/loadavg) $(free -m | awk '/Mem/{print "used="$3}') mgmtfails=$fails" >>"$RUNNER_TEMP/res.log"
    sleep 3
  done
}
watcher >"$RUNNER_TEMP/watch.log" 2>&1 & W=$!
start=$(now)
timeout 2300 "$BIN/mdevenv" test --name cs >"$RUNNER_TEMP/test.log" 2>&1
obs "devenv test with sequential-capacity hook: exit $? after $(since "$start")s"
kill $W
grep -E '^\[|OBS|Error|logs:' "$RUNNER_TEMP/test.log" | tail -30
echo "--- watcher"; cat "$RUNNER_TEMP/watch.log"
echo "--- resources (every 30th sample)"; awk 'NR % 10 == 1' "$RUNNER_TEMP/res.log"
logs=$dir/logs
if [ -d "$logs/mgmt" ]; then
  for f in $(find "$logs/mgmt" -path '*containers*' \( -name 'kube-apiserver*' -o -name 'etcd*' \) | head -4); do
    echo "--- $f"; tail -20 "$f" | cut -c1-300
  done
  grep -h -i -E 'oom|out of memory|killed process' "$logs"/mgmt/*/journal.log 2>/dev/null | tail -5
  grep -h -E 'probe failed' "$logs"/mgmt/*/kubelet.log 2>/dev/null | tail -8 | cut -c1-300
fi
summary
