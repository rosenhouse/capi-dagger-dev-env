#!/usr/bin/env bash
# Config mistakes that validation lets through, and a caller's deadline that ends during bring-up.
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
    obs "$1: up ended after $((SECONDS - t))s; last stage: $(grep -E '^\[ *[0-9.]+s\]' "$log" | grep -vE 'tearing down|from inside' | tail -1)"
    obs "$1: error: $(grep -vE '^\[ *[0-9.]+s\]|^\s*$|OBS-HOOK' "$log" | grep -vE '^\s+/|^goroutine|^created by|^\s' | head -6 | tr '\n' '|' | cut -c1-900)"
    echo "--- tail of $1"; tail -20 "$log" | cut -c1-400
  fi
}

expect_fail name-upper lt 900
expect_fail non-main lt 900
expect_fail hook-dockerfile-missing lt 900
printf 'package main\n\nvar version string\n\nfunc main() { println(version) }\n' > main.go
expect_fail root-cmd lt 900
rm main.go
expect_fail refname-short lt 900
expect_fail bad-config lt 1200

# Two packages with one RefName: does each PackageInstall get its own bundle?
pid=$(DEVENV_SCENARIO=dup-refname "$repo/explore/lib/up-bg.sh" "$RUNNER_TEMP/dup.log" 1200 -- "$D" up --name lt 2>/dev/null)
if [ -n "$pid" ]; then
  obs "dup-refname: came up"
  export KUBECONFIG=$C/.devenv/lt/mgmt.kubeconfig
  obs "dup-refname: manager Deployment: $(kubectl get deploy -n legacyctl manager -o name 2>&1)"
  obs "dup-refname: Packages: $(kubectl get packages -A -o custom-columns=NAME:.metadata.name,BUNDLE:.spec.template.spec.fetch[0].imgpkgBundle.image --no-headers 2>&1 | tr '\n' '|')"
  obs "dup-refname: Apps: $(kubectl get apps -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,BUNDLE:.spec.fetch[0].imgpkgBundle.image,DESC:.status.friendlyDescription --no-headers 2>&1 | tr '\n' '|')"
  "$D" down --name lt 2>&1 | tail -1
  while kill -0 "$pid" 2>/dev/null; do sleep 2; done
else
  obs "dup-refname: up failed: $(tail -4 "$RUNNER_TEMP/dup.log" | tr '\n' '|')"
fi

# A caller's deadline that ends during the workload cluster gate.
t=$SECONDS
"$RUNNER_TEMP/lifecycle" deadline dl .devenv 100s > "$RUNNER_TEMP/deadline.log" 2>&1
obs "deadline: exit $? after $((SECONDS - t))s"
grep -E "^OBS" "$RUNNER_TEMP/deadline.log" | while read -r l; do obs "${l#OBS: }"; done
tail -12 "$RUNNER_TEMP/deadline.log" | cut -c1-400
obs "deadline: logs dir: $(ls "$C/.devenv/dl/logs" 2>/dev/null | wc -l) entries"
summary
