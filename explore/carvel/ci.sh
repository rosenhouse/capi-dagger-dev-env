#!/usr/bin/env bash
# 1. devenv test while the addon-manager's remote PackageInstall cannot find a matching agent version.
# 2. Two management packages whose namespace-less resources share a name, as production intoNs keeps apart.
source "$(dirname "$0")/common.sh"
NAME=ci

section setup
setup_consumer

section "devenv test with the production constraint >=1.0.0"
t0=$SECONDS
(cd "$A" && DEVENV_SCENARIO=base timeout 1500 "$DEVENV" test --name "$NAME") >"$W/test.log" 2>&1
obs "test rc=$? took $((SECONDS - t0))s"
sed 's/^/    | /' "$W/test.log" | tail -15

section "two packages with a namespace-less ConfigMap of the same name"
DEVENV_SCENARIO=collide up_fg collide
d=$A/.devenv/$NAME/logs
[ -f "$d/resources.yaml" ] && yq '.items[] | select(.kind == "App") | "App " + .metadata.namespace + "/" + .metadata.name + ": " + ((.status.usefulErrorMessage // .status.friendlyDescription // "") | tostring)' "$d/resources.yaml" | head -c 3000 | sed 's/^/OBS: /'

section teardown
down_purge
summary
