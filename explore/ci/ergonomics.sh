#!/usr/bin/env bash
# CI ergonomics: cold and warm timing, output in a non-TTY log, failure logs, and signals mid bring-up.
source "$(dirname "$0")/lib.sh"
G=$REPO/examples/greeting
D=$T/devenv
cd "$G" && go build -o "$D" ./cmd/devenv || exit 1
obs "runner: $(nproc) cpus, $(free -g | awk '/Mem/{print $2}') GB, disk free $(df -BG --output=avail / | tail -1), kubectl $(command -v kubectl)"

# 1. Cold and warm test with one name.
cd "$G"
run cold "$D" test --name ci
obs "cold: control chars in stderr: $(grep -c $'\r\|\x1b' "$T/cold.err"); stage lines: $(grep -c '^\[' "$T/cold.err")"
obs "cold: preflight reached at $(grep -m1 '] preflight' "$T/cold.err" | cut -c1-9)"
obs "cold: after pass, .devenv/ci holds: $(ls -A .devenv/ci | tr '\n' ' '); dagger.log $(wc -c <.devenv/ci/dagger.log) bytes"
echo "--- dagger.log head"; head -20 .devenv/ci/dagger.log
run warm "$D" test --name ci
docker system df
run purge "$D" down --purge --name ci
run purge-again "$D" down --purge --name ci

# 2. A failing Test: exit code, message, and whether logs capture both clusters' pods.
F=$T/failing
"$REPO/explore/lib/adopter.sh" "$G" "$F" >/dev/null 2>&1 || obs "adopter.sh failed"
cp "$REPO/explore/ci/fail.go.txt" "$F/cmd/devenv/zz_fail.go"
(cd "$F" && GOWORK=off go build -o "$T/devenv-fail" ./cmd/devenv) || obs "failing consumer did not build"
cd "$F"
run failtest "$T/devenv-fail" test --name fail
L=$F/.devenv/fail
obs "failtest: logs dir $(du -sh "$L" | cut -f1), $(find "$L" -type f | wc -l) files"
find "$L" -maxdepth 3 | sed "s|$L|.|" | head -60
for m in CRASHER-WORKLOAD-MARKER CRASHER-MGMT-MARKER; do
  obs "failtest: files mentioning $m: $(grep -rl "$m" "$L" 2>/dev/null | sed "s|$L/||" | tr '\n' ' ')"
done
obs "failtest: files mentioning greeting-controller pod logs: $(grep -rl 'greeting-controller' "$L" 2>/dev/null | sed "s|$L/||" | head -8 | tr '\n' ' ')"
obs "failtest: clusters exported: $(ls "$L/logs" 2>/dev/null | tr '\n' ' ')"
run failtest-purge "$T/devenv-fail" down --purge --name fail

# 3. SIGTERM mid bring-up of a randomly named test, as `timeout` or a CI cancel sends.
cd "$G"
mb0=$(engine_mb)
"$D" test >"$T/sigterm.out" 2>"$T/sigterm.err" &
pid=$!
if wait_for "$T/sigterm.err" '] workload cluster' 900; then
  t0=$SECONDS
  kill -TERM $pid
  wait $pid; rc=$?
  obs "sigterm: exit=$rc $((SECONDS - t0))s after SIGTERM during 'workload cluster'; engine disk $mb0 -> $(engine_mb)MB"
else
  obs "sigterm: never reached workload cluster"; kill -TERM $pid; wait $pid
fi
tail -12 "$T/sigterm.err"
"$D" status
obs "sigterm: .devenv now holds: $(ls .devenv | tr '\n' ' '); dagger processes: $(pgrep -fa 'dagger' | grep -v engine | wc -l)"
for env in $(ls .devenv); do
  run "sigterm-purge-$env" "$D" down --purge --name "$env"
done
obs "sigterm: engine disk after purge $(engine_mb)MB"

# 4. GitHub Actions cancel: SIGINT, then SIGTERM 7.5 s later. Then rerun the same name.
"$D" test --name cancel >"$T/cancel.out" 2>"$T/cancel.err" &
pid=$!
if wait_for "$T/cancel.err" '] management cluster' 600; then
  pause 20
  t0=$SECONDS
  kill -INT $pid
  sleep 7.5
  alive=no; kill -0 $pid 2>/dev/null && { alive=yes; kill -TERM $pid; }
  wait $pid; rc=$?
  obs "cancel: exit=$rc, alive 7.5s after SIGINT: $alive, gone after $((SECONDS - t0))s"
fi
tail -8 "$T/cancel.err"
run rerun-after-cancel "$D" test --name cancel
run cancel-purge "$D" down --purge --name cancel
finish
