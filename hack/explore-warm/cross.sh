#!/usr/bin/env bash
# Restore another runner's checkpoint, copied under this host's key.
source hack/explore-warm/lib.sh
cpu
watch_data_dirs

note "Another runner's checkpoint"
say "- donor: $(cat "$RUNNER_TEMP/donor/donor.txt")"
key=$("$D" platform key)
mkdir -p "$CACHE"
mv "$RUNNER_TEMP"/donor/*.checkpoint "$CACHE/$key.checkpoint"
timed cross-test timeout 1500 "$D" test --name cross
ls -la .devenv/cross/logs/restore 2>&1 | quote
leftovers
