#!/usr/bin/env bash
# Job C: build-side probes, without Kind.
HERE=$(cd "$(dirname "$0")" && pwd)
W=${RUNNER_TEMP:-/tmp}/probeC
mkdir -p "$W"
export GOWORK=off
(cd "$HERE/probe" && go build -o "$W/probe" .) || { echo "OBS: probe failed to build"; exit 1; }
"$W/probe" "$W" 2>&1 | tee "$W/out.txt"
echo "================ SUMMARY ================"
grep '^OBS:' "$W/out.txt"
