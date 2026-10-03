#!/usr/bin/env bash
# Config that a Carvel team might bring as is: required values, an imgpkg bundle root, a plain values file,
# then a RefName and a Name that kapp-controller or the registry may reject.
source "$(dirname "$0")/common.sh"
NAME=fail

stage_times() { grep -E '^\[ *[0-9.]+s\]' "$W/up-$1.log" | tail -6 | sed 's/^/    stage| /'; }

section setup
setup_consumer

section "management packages that need values, or carry a bundle root or a plain values file"
DEVENV_SCENARIO=failures up_fg failures
stage_times failures
dump_state
d=$A/.devenv/$NAME/logs
if [ -f "$d/resources.yaml" ]; then
  yq '.items[] | select(.kind == "App") | "App " + .metadata.namespace + "/" + .metadata.name + ": " + ((.status.usefulErrorMessage // .status.friendlyDescription // "") | tostring)' "$d/resources.yaml" | head -c 4000 | sed 's/^/OBS: /'
fi

section "RefName without dots"
DEVENV_SCENARIO=badref up_fg badref
stage_times badref

section "package Name with uppercase and underscore"
DEVENV_SCENARIO=badname up_fg badname
stage_times badname

section teardown
down_purge
summary
