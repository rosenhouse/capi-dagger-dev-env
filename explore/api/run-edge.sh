#!/usr/bin/env bash
# Config and build edge cases that fail during bring-up, a workload-only config, and a deadline on Up.
source "$(dirname "$0")/common.sh"
setup
D=$RUNNER_TEMP/devenv

# expect_fail SCENARIO NAME TIMEOUT: runs up and reports how and when it ends.
expect_fail() {
  local t=$SECONDS log=$RUNNER_TEMP/$1.log pid
  pid=$(DEVENV_SCENARIO=$1 "$repo/explore/lib/up-bg.sh" "$log" "$3" -- "$D" up --name "$2" 2>/dev/null)
  if [ -n "$pid" ]; then
    obs "$1: came up after $((SECONDS - t))s"
    "$D" down --name "$2" 2>&1 | tail -1
    while kill -0 "$pid" 2>/dev/null; do sleep 2; done
  else
    obs "$1: up ended after $((SECONDS - t))s; last stage: $(grep -E '^\[ *[0-9.]+s\]' "$log" | grep -v 'tearing down' | tail -1)"
    obs "$1: error: $(grep -vE '^\[ *[0-9.]+s\]|^\s*$' "$log" | grep -vE '^\s+/|^goroutine|^created by|^\s' | head -6 | tr '\n' '|' | cut -c1-700)"
  fi
  echo "--- tail of $1"; tail -20 "$log" | cut -c1-400
}

expect_fail images-nil ec 900
expect_fail image-missing ec 900

# A consumer on a newer Go patch release than the tool's build image.
cp go.mod go.mod.orig
go mod edit -go=1.26.3
obs "newer-go: host builds cmd/manager: $(GOTOOLCHAIN=auto go build -o /dev/null ./cmd/manager 2>&1 && echo ok)"
expect_fail default ec 900
mv go.mod.orig go.mod

# A command that imports a sibling module through a replace directive, as monorepos do.
mkdir -p "$RUNNER_TEMP/api" cmd/usesapi
printf 'module example.com/api\n\ngo 1.26.1\n' > "$RUNNER_TEMP/api/go.mod"
printf 'package api\n\nconst Name = "api"\n' > "$RUNNER_TEMP/api/api.go"
printf 'package main\n\nimport "example.com/api"\n\nvar version string\n\nfunc main() { println(api.Name, version) }\n' > cmd/usesapi/main.go
cp go.mod go.mod.orig
go mod edit -require=example.com/api@v0.0.0 -replace=example.com/api=../api
obs "sibling: host builds cmd/usesapi: $(go build -o /dev/null ./cmd/usesapi 2>&1 && echo ok)"
expect_fail sibling ec 900
mv go.mod.orig go.mod && rm -rf cmd/usesapi

# Only Workload packages, as a team whose controller runs outside the cluster would start.
expect_fail workload-only wo 1500
tails "$C/.devenv/wo"

# A caller's deadline that ends during bring-up.
t=$SECONDS
"$RUNNER_TEMP/lifecycle" deadline dl .devenv 240s > "$RUNNER_TEMP/deadline.log" 2>&1
obs "deadline: exit $? after $((SECONDS - t))s"
grep -E "^OBS" "$RUNNER_TEMP/deadline.log" | while read -r l; do obs "${l#OBS: }"; done
tail -12 "$RUNNER_TEMP/deadline.log" | cut -c1-400
obs "deadline: logs dir: $(ls "$C/.devenv/dl/logs" 2>/dev/null | wc -l) entries"
summary
