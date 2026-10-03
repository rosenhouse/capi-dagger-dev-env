#!/usr/bin/env bash
# What a CI job can upload after a failing Test, and what the host tunnels expose.
source "$(dirname "$0")/lib.sh"
G=$REPO/examples/greeting
D=$T/devenv
cd "$G" && go build -o "$D" ./cmd/devenv || exit 1

# A copy of the example whose Test leaves a marker pod in each cluster and fails.
# The host build uses a go.work outside the copy, so the in-session build sees only the copy's go.mod.
F=$T/failing
cp -a "$G" "$F"
cp "$REPO/explore/ci/fail.go.txt" "$F/cmd/devenv/zz_fail.go"
printf 'go 1.26.1\n\nuse (\n\t%s\n\t%s\n)\n' "$F" "$REPO" >"$T/fail.work"
(cd "$F" && GOWORK=$T/fail.work go build -o "$T/devenv-fail" ./cmd/devenv) || obs "failing consumer did not build"
cd "$F"
run failtest "$T/devenv-fail" test --name fail
L=$F/.devenv/fail
obs "failtest: logs dir $(du -sh "$L" | cut -f1), $(find "$L" -type f | wc -l) files; clusters exported: $(ls "$L/logs" 2>/dev/null | tr '\n' ' ')"
find "$L" -maxdepth 4 | sed "s|$L|.|" | head -70
for m in CRASHER-WORKLOAD-MARKER CRASHER-MGMT-MARKER; do
  obs "failtest: files mentioning $m: $(grep -rl "$m" "$L" 2>/dev/null | sed "s|$L/||" | tr '\n' ' ')"
done
obs "failtest: files with greeting-controller (workload) pod logs: $(grep -rl 'greeting-controller' "$L/logs" 2>/dev/null | sed "s|$L/||" | head -8 | tr '\n' ' ')"
obs "failtest: kinds in resources.yaml: $(grep -h '^  kind:' "$L/logs/resources.yaml" 2>/dev/null | sort | uniq -c | tr '\n' ' ')"
obs "dagger.log: $(wc -l <"$L/dagger.log") lines, $(grep -c 'level=debug' "$L/dagger.log") registry debug lines, $(wc -c <"$L/dagger.log") bytes"

# Tunnels: which host ports listen on all interfaces, and what an unauthenticated client reaches.
cd "$G"
pid=$("$REPO/explore/lib/up-bg.sh" "$T/up.log" 900 -- "$D" up --name tun) || { obs "up failed"; tail -20 "$T/up.log"; finish; exit 0; }
ip=$(ip -4 addr show "$iface" | awk '/inet /{sub(/\/.*/,"",$2); print $2; exit}')
sudo ss -ltnp | grep -E 'dagger|devenv' | tee "$T/ports.txt"
for port in $(awk '$4 ~ /^(0\.0\.0\.0|\*|\[::\]):/ {n=split($4,a,":"); print a[n]}' "$T/ports.txt" | sort -u); do
  obs "tunnel $ip:$port from the network: /version $(curl -sk -m 5 -o /dev/null -w '%{http_code}' "https://$ip:$port/version") /api $(curl -sk -m 5 -o /dev/null -w '%{http_code}' "https://$ip:$port/api")"
done
obs "kubeconfig servers: $(grep -h 'server:' .devenv/tun/*.kubeconfig | tr -s ' ' | tr '\n' ' ')"
"$D" down --purge --name tun >/dev/null 2>&1
wait "$pid"
finish
