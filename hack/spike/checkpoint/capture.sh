#!/usr/bin/env bash
# E2 capture: checkpoints a tiny VM into DIR with this host's CPU identity.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
out=$1
mkdir -p "$out"

summary "| measurement | value |"
summary "|---|---|"
boot_tiny tiny 18080
wait_serving 18080
checkpoint tiny "$out/tiny.checkpoint"
smolvm machine delete --name tiny -f

cpu_model >"$out/model.txt"
file_contract "$out/tiny.checkpoint" >"$out/contract.txt"
echo "image ${ImageVersion:-?}, kernel $(uname -r), $(grep -m1 microcode /proc/cpuinfo | tr -d '\t')" >"$out/host.txt"
grep -m1 '^flags' /proc/cpuinfo >"$out/flags.txt"
measure "CPU" "$(cat "$out/model.txt")"
measure "host" "$(cat "$out/host.txt")"
measure "contract in checkpoint" "$(cat "$out/contract.txt")"
measure "contract from host_contract" "$(host_contract)"
[ "$(host_contract)" = "$(cat "$out/contract.txt")" ]
