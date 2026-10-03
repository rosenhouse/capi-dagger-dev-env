#!/usr/bin/env bash
# devenv test with a Ready hook that installs Flux and a Test hook that deploys and redeploys through Flux, then fails.
source "$(dirname "$0")/lib.sh"
setup_consumer
cd "$W" || exit 1
export FLUXCO_SCENARIO=refs FLUXCO_READY=$TOOL/explore/flux/ready-flux.sh FLUXCO_TEST=$TOOL/explore/flux/test-flux.sh FLUXCO_FAIL=1 D TOOL
t=$SECONDS
"$D" test --name rt > "$RUNNER_TEMP/test-rt.log" 2>&1
rc=$?
obs "devenv test with Flux hooks (Test hook fails on purpose at the end): exit=$rc after $(since $t)s"
grep -vE '^\s*$|PodSecurity' "$RUNNER_TEMP/test-rt.log" | tail -40
grep -h '^OBS:' "$RUNNER_TEMP/test-rt.log" | sed 's/^OBS: /hook: /' > "$RUNNER_TEMP/hook-obs.txt"
while read -r line; do obs "$line"; done < "$RUNNER_TEMP/hook-obs.txt"

echo "=== What the failed test left for debugging"
L=$W/.devenv/rt/logs
find "$L" -maxdepth 2 | sed "s#$W/##" | head -30
obs "exported log dirs: $(ls "$L" | tr '\n' ' ')"
obs "resources.yaml kinds: $(grep -E '^  kind: ' "$L/resources.yaml" | sort | uniq -c | tr -s ' ' | tr '\n' ';')"
obs "resources.yaml mentions kustomize.toolkit.fluxcd.io: $(grep -c 'kustomize.toolkit.fluxcd.io' "$L/resources.yaml")"
obs "flux-system container logs exported: $(find "$L" -path '*containers*' -name '*_flux-system_*' | wc -l)"
obs "workload cluster dirs under logs: $(find "$L" -maxdepth 1 -type d -name 'work*' | tr '\n' ' ')"
"$D" status
summary
