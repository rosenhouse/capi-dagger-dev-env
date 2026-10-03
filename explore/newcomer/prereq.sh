#!/usr/bin/env bash
# Prerequisite failures, first-run engine start, -v output, running from a subdirectory, and README's test.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
ensure_kubectl
cd "$here/../../examples/greeting"
go build -o /tmp/devenv ./cmd/devenv
D() { /tmp/devenv "$@"; }
obs "docker images before: $(docker images --format '{{.Repository}}:{{.Tag}}' | tr '\n' ' ')"

echo "################ low inotify instances (first run also starts the Dagger engine)"
orig=$(cat /proc/sys/fs/inotify/max_user_instances)
sudo sysctl -w fs.inotify.max_user_instances=128
timed_tail "up with max_user_instances=128" 20 D up --name ino
sudo sysctl -w fs.inotify.max_user_instances="$orig"
cat .devenv/ino/dagger.log | tail -5

echo "################ up -v from a subdirectory of the module"
cd config
start_up "$RUNNER_TEMP/verbose.log" /tmp/devenv up --name sub -v
wait_up "$RUNNER_TEMP/verbose.log" 1500
note "-v up output: $(wc -l < "$RUNNER_TEMP/verbose.log") lines, $(du -h "$RUNNER_TEMP/verbose.log" | cut -f1)"
echo "--- stage lines"; grep -E '^\[ *[0-9.]+s\]' "$RUNNER_TEMP/verbose.log"
echo "--- -v sample (first 40 lines)"; head -40 "$RUNNER_TEMP/verbose.log" | cut -c1-200
echo "--- -v sample (around management cluster)"; grep -n -m3 -A8 'kind create' "$RUNNER_TEMP/verbose.log" | cut -c1-200
echo "--- -v tail"; tail -30 "$RUNNER_TEMP/verbose.log" | cut -c1-200
note "dagger.log: $(wc -l < .devenv/sub/dagger.log) lines"
find "$here/../../examples/greeting" -name .devenv -type d
cd ..
echo "--- from the module root"
D status
D kubeconfig --name sub 2>&1 | tail -1; note "kubeconfig --name sub from module root: $(D kubeconfig --name sub 2>&1 | tail -1)"
D down --name sub 2>&1 | tail -1
echo "--- from config/"
(cd config && D status && timed "down from config/" D down --name sub --purge)

echo "################ README test, no --name, via go run"
timed "go run ./cmd/devenv test" go run ./cmd/devenv test
ls -la .devenv
D status
summary
