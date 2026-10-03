#!/usr/bin/env bash
# A module that declares a newer Go than devenv's builder; down while up is still coming up.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
cd "$here/../../examples/greeting"
go build -o /tmp/devenv ./cmd/devenv
D() { /tmp/devenv "$@"; }

echo "################ down while up is still coming up"
start_up "$RUNNER_TEMP/d1.log" /tmp/devenv up --name d1
until grep -q 'management cluster' "$RUNNER_TEMP/d1.log" || ! kill -0 "$UP_PID" 2>/dev/null; do sleep 2; done
s=$SECONDS
timed "down during bring-up" D down --name d1
while kill -0 "$UP_PID" 2>/dev/null; do sleep 1; done
note "up exited $((SECONDS - s))s after down was issued"
cat "$RUNNER_TEMP/d1.log"

echo "################ go.mod declares go 1.26.4"
cp -a . "$RUNNER_TEMP/g"
cd "$RUNNER_TEMP/g"
rm -rf .devenv
sed -i 's/^go 1\.26\.[0-9]*$/go 1.26.4/' go.mod
grep -n '^go \|^toolchain' go.mod
timed_tail "up with go 1.26.4 in go.mod" 30 D up --name gover
echo "################ go.mod has toolchain go1.26.4"
sed -i 's/^go 1\.26\.4$/go 1.26.1\ntoolchain go1.26.4/' go.mod
grep -n '^go \|^toolchain' go.mod
start_up "$RUNNER_TEMP/tc.log" /tmp/devenv up --name tc
wait_up "$RUNNER_TEMP/tc.log" 900; cat "$RUNNER_TEMP/tc.log" | tail -15
D down --name tc --purge
summary
