#!/usr/bin/env bash
# Six extra clusters deleted at once, from a Test hook, so a failure exports in-session diagnostics.
source "$(dirname "$0")/lib.sh"
install_tools
src=$RUNNER_TEMP/consumer
cp -a "$GREETING" "$src"
rm -rf "$src/.devenv"
cp "$REPO/explore/multicluster/consumer/massdelete.go.txt" "$src/cmd/devenv/massdelete.go"
sed -i 's/Test:  greeting,/Test:  massDelete,/' "$src/cmd/devenv/main.go"
cd "$src" || exit 1
export GOWORK=off
go mod edit -droprequire=github.com/rosenhouse/capi-dagger-dev-env
go get github.com/rosenhouse/capi-dagger-dev-env@2eacde6 >/dev/null 2>&1
go mod tidy
go build -o "$BIN/mdevenv" ./cmd/devenv || { obs "consumer build failed"; summary; exit 1; }
(while true; do echo "$(date +%H:%M:%S) load=$(cut -d' ' -f1 /proc/loadavg) $(free -m | awk '/Mem/{print "used="$3" avail="$7}')"; sleep 15; done >"$RUNNER_TEMP/res.log") & RES=$!
start=$(now)
timeout 2500 "$BIN/mdevenv" test --name mdh >"$RUNNER_TEMP/test.log" 2>&1
obs "devenv test with mass-delete hook: exit $? after $(since "$start")s"
kill $RES
grep -E '^\[|OBS|first:|last:|Error|logs:' "$RUNNER_TEMP/test.log" | tail -40
cat "$RUNNER_TEMP/res.log" | tail -60
logs=$src/.devenv/mdh/logs
if [ -d "$logs" ]; then
  find "$logs/mgmt" -maxdepth 3 | head -40
  for f in $(find "$logs/mgmt" -path '*containers*' \( -name 'kube-apiserver*' -o -name 'etcd*' \) | head -4); do
    echo "--- $f"; tail -25 "$f" | cut -c1-300
  done
  grep -h -i -E 'oom|out of memory|killed process' "$logs"/mgmt/*/journal.log 2>/dev/null | tail -10
  grep -h -E 'Liveness probe failed|Readiness probe failed' "$logs"/mgmt/*/kubelet.log 2>/dev/null | tail -10 | cut -c1-300
fi
summary
