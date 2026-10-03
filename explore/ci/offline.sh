#!/usr/bin/env bash
# Warm cache, then lose access to one or all upstream registries: does a warm environment still come up?
source "$(dirname "$0")/lib.sh"
G=$REPO/examples/greeting
D=$T/devenv
cd "$G" && go build -o "$D" ./cmd/devenv || exit 1
run warmup "$D" test --name w

block() { # comment, iptables match
  sudo iptables -I DOCKER-USER -i docker0 ! -o docker0 "$@" -j REJECT --reject-with tcp-reset
}
# 1. ghcr.io unreachable: kapp-controller's image is already in the ghcr.io mirror's cache.
for ip in $(getent ahostsv4 ghcr.io | awk '{print $1}' | sort -u); do block -d "$ip" -p tcp; done
obs "blocked ghcr.io: $(getent ahostsv4 ghcr.io | awk '{print $1}' | sort -u | tr '\n' ' ')"
run ghcr-down timeout 900 "$D" test --name w
obs "ghcr-down: stages: $(grep -o '] [a-z].*' "$T/ghcr-down.err" | cut -c3-40 | tr '\n' '|')"
echo "--- dagger.log tail"; tail -15 .devenv/w/dagger.log

# 2. No internet at all (a laptop on a plane), still warm.
block -p tcp -m multiport --dports 80,443
run offline timeout 900 "$D" test --name w
obs "offline: stages: $(grep -o '] [a-z].*' "$T/offline.err" | cut -c3-40 | tr '\n' '|')"
echo "--- dagger.log tail"; tail -15 .devenv/w/dagger.log
finish
