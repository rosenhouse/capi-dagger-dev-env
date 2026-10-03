#!/usr/bin/env bash
# Hook runtime behavior through the consumer's devenv CLI.
source "$(dirname "$0")/common.sh"
setup
D=$RUNNER_TEMP/devenv
sock_dir=$HOME/.cache/devenv

run_test() { # scenario
  local t=$SECONDS
  rm -rf "$C/.devenv/hk/logs"
  DEVENV_SCENARIO=$1 "$D" test --name hk > "$RUNNER_TEMP/$1.log" 2>&1
  local rc=$?
  obs "$1: devenv test exit $rc after $((SECONDS - t))s"
  grep -E "OBS-HOOK" "$RUNNER_TEMP/$1.log" | while read -r l; do obs "$1: $l"; done
  echo "--- tail of $1 output"; tail -25 "$RUNNER_TEMP/$1.log" | cut -c1-400
  obs "$1: last lines: $(grep -v '^\s*$' "$RUNNER_TEMP/$1.log" | grep -vE '^\s+/|^goroutine|^main\.|^github|^created by|^\s' | tail -4 | tr '\n' '|' | cut -c1-500)"
  obs "$1: logs dir exported: $(ls "$C/.devenv/hk/logs" 2>/dev/null | wc -l) entries, newest $(stat -c %y "$C/.devenv/hk/logs" 2>/dev/null)"
  obs "$1: status: $("$D" status 2>&1 | tail -n +2 | tr '\n' '|')"
  obs "$1: sockets: $(ls "$sock_dir" 2>/dev/null | tr '\n' ' ')"
}

run_test ready-error
run_test test-panic

# A hung Test, interrupted as Ctrl-C would.
t=$SECONDS
DEVENV_SCENARIO=test-hang "$D" test --name hk > "$RUNNER_TEMP/test-hang.log" 2>&1 &
pid=$!
until grep -q "Test started" "$RUNNER_TEMP/test-hang.log" || ! kill -0 $pid 2>/dev/null || [ $((SECONDS - t)) -gt 1200 ]; do sleep 5; done
obs "test-hang: Test started after $((SECONDS - t))s: $(grep 'Test started' "$RUNNER_TEMP/test-hang.log")"
"$repo/explore/lib/pause.sh" 20
rm -rf "$C/.devenv/hk/logs"
ti=$SECONDS
kill -INT $pid
wait $pid
obs "test-hang: exit $? $((SECONDS - ti))s after SIGINT"
grep -E "OBS-HOOK" "$RUNNER_TEMP/test-hang.log" | while read -r l; do obs "test-hang: $l"; done
tail -15 "$RUNNER_TEMP/test-hang.log" | cut -c1-400
obs "test-hang: logs dir after interrupt: $(ls "$C/.devenv/hk/logs" 2>/dev/null | wc -l) entries"

# Is Ready called again on redeploy?
t=$SECONDS
pid=$("$repo/explore/lib/up-bg.sh" "$RUNNER_TEMP/up.log" 1200 -- "$D" up --name hk)
obs "up: pid '$pid' after $((SECONDS - t))s"
if [ -n "$pid" ]; then
  obs "up: hint lines: $(grep -E 'redeploy|Ctrl-C' "$RUNNER_TEMP/up.log" | tr '\n' '|')"
  "$D" redeploy --name hk > "$RUNNER_TEMP/redeploy.log" 2>&1
  obs "redeploy exit $?: $(tail -3 "$RUNNER_TEMP/redeploy.log" | tr '\n' '|')"
  grep -E "OBS-HOOK" "$RUNNER_TEMP/up.log" | while read -r l; do obs "up: $l"; done
  "$D" down --name hk --purge 2>&1 | tail -3
  while kill -0 "$pid" 2>/dev/null; do sleep 2; done
  obs "up output tail: $(tail -3 "$RUNNER_TEMP/up.log" | tr '\n' '|')"
else
  tails "$C/.devenv/hk"
fi
summary
