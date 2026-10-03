#!/usr/bin/env bash
# devenv embedded as a subcommand of an existing CLI, and a go test flow whose module ignores .devenv/.
source "$(dirname "$0")/common.sh"
setup
go build -o "$RUNNER_TEMP/mytool" ./cmd/mytool || exit 1
M=$RUNNER_TEMP/mytool

t=$SECONDS
pid=$("$repo/explore/lib/up-bg.sh" "$RUNNER_TEMP/embed-up.log" 1200 -- "$M" devenv up --name em)
obs "embed up: pid '$pid' after $((SECONDS - t))s"
obs "embed up: printed: $(grep -vE '^\[|OBS-HOOK' "$RUNNER_TEMP/embed-up.log" | tr '\n' '|')"
if [ -n "$pid" ]; then
  "$M" devenv redeploy --name em > "$RUNNER_TEMP/embed-redeploy.log" 2>&1
  obs "embed redeploy exit $?: $(tail -2 "$RUNNER_TEMP/embed-redeploy.log" | tr '\n' '|')"
  "$M" devenv down --name em 2>&1 | tail -1
  while kill -0 "$pid" 2>/dev/null; do sleep 2; done
fi

t=$SECONDS
DEVENV_SCENARIO=test-fail "$M" devenv test > "$RUNNER_TEMP/embed-test.log" 2>&1
obs "embed test (failing Test, random name): exit $? after $((SECONDS - t))s"
obs "embed test printed: $(grep -vE '^\[|OBS-HOOK' "$RUNNER_TEMP/embed-test.log" | tr '\n' '|')"
name=$(grep -oE 'env-[a-z0-9]{6}' "$RUNNER_TEMP/embed-test.log" | head -1)
"$M" devenv down --purge --name "$name" 2>&1 | tail -2

# The go test flow again, with .devenv/ ignored the way a consumer might.
printf '.devenv/\n' > .gitignore
t=$SECONDS
go test ./e2e -count=1 -v -timeout 35m -run 'TestRedeployWithoutSourceChange' > "$RUNNER_TEMP/gotest-ignored.log" 2>&1
obs "go test with .devenv/ ignored: exit $? after $((SECONDS - t))s"
grep -E "^OBS: redeploy" "$RUNNER_TEMP/gotest-ignored.log" | while read -r l; do obs "ignored: ${l#OBS: }"; done
grep -E "OBS-HOOK: Images" "$RUNNER_TEMP/gotest-ignored.log" | while read -r l; do obs "ignored: $l"; done
summary
