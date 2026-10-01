#!/usr/bin/env bash
# Ctrl-C during each stage of a restore, and during a save's capture; then a test must still start warm.
source hack/explore-warm/lib.sh
cpu
watch_data_dirs

timed save "$D" platform save
ckpt=$(ls "$CACHE"/*.checkpoint)
sum=$(sha256sum "$ckpt" | cut -d' ' -f1)
leftovers

note "Ctrl-C 20 s into create --from"
interrupt restore "\] restore VM$" 20 "$D" up --name i1
leftovers

note "Ctrl-C as the restored VM starts"
interrupt start "\] start VM$" 0 "$D" up --name i2
leftovers

note "Ctrl-C while the first-party phase pushes"
interrupt push "\] push images and bundles$" 1 "$D" up --name i3
leftovers

note "up again after the interrupts"
timed up-again "$D" up --name i1
timed up-again-down "$D" down --name i1 --purge
leftovers

note "Ctrl-C 10 s into platform save's capture"
interrupt capture "\] capture$" 10 "$D" platform save --force
say "- checkpoint unchanged: $([ "$(sha256sum "$ckpt" | cut -d' ' -f1)" = "$sum" ] && echo yes || echo no)"
ls -la "$CACHE" "$HOME/.cache/devenv" | quote
leftovers

note "test after everything"
timed test "$D" test
ls -la .devenv | quote
leftovers
