#!/usr/bin/env bash
# go test-driven e2e: TestMain calls devenv.Up; then a program that bounds only Up's context.
source "$(dirname "$0")/common.sh"
setup

t=$SECONDS
go test ./e2e -count=1 -v -timeout 35m 2>&1 | tee "$RUNNER_TEMP/gotest.log" | grep -E "^(OBS|---|===|ok|FAIL|PASS|panic)|stage|Error|error" | cut -c1-400
rc=${PIPESTATUS[0]}
obs "go test e2e exit $rc after $((SECONDS - t))s"
grep -E "^OBS" "$RUNNER_TEMP/gotest.log" | while read -r l; do obs "gotest ${l#OBS: }"; done
grep -E "OBS-HOOK" "$RUNNER_TEMP/gotest.log" | while read -r l; do obs "gotest ${l}"; done
obs "state dirs in module: $(find "$C" -name .devenv -type d | sed "s#$C/##" | tr '\n' ' ')"
obs "git status after go test: $(git -C "$C" status --short | tr '\n' ' ')"
[ "$rc" = 0 ] || { tails "$C/e2e/.devenv/e2e"; tail -60 "$RUNNER_TEMP/gotest.log"; }

t=$SECONDS
"$RUNNER_TEMP/lifecycle" bounded bd .devenv > "$RUNNER_TEMP/bounded.log" 2>&1
obs "lifecycle bounded exit $? after $((SECONDS - t))s"
grep -E "^OBS" "$RUNNER_TEMP/bounded.log" | while read -r l; do obs "${l#OBS: }"; done
tail -25 "$RUNNER_TEMP/bounded.log" | cut -c1-400
tails "$C/.devenv/bd"
summary
