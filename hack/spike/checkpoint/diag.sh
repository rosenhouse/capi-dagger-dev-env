#!/usr/bin/env bash
# Collects smolvm and kernel diagnostics into DIR.
set -uo pipefail
dir=$1
mkdir -p "$dir"
smolvm machine ls --verbose >"$dir/machines.txt" 2>&1
sudo dmesg >"$dir/host-dmesg.txt" 2>&1
for m in $(smolvm machine ls -q); do
  d=$(smolvm machine data-dir --name "$m")
  cp "$d/agent-console.log" "$dir/$m-console.log" 2>/dev/null
  cp "$d/agent-startup-error.log" "$dir/$m-startup-error.log" 2>/dev/null
  ls -la "$d" >"$dir/$m-data-dir.txt" 2>&1
  smolvm machine exec --name "$m" --timeout 20s -- dmesg >"$dir/$m-guest-dmesg.txt" 2>&1
done
cp "$RUNNER_TEMP"/*.log "$dir/" 2>/dev/null
true
