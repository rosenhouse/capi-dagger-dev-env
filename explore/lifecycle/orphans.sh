#!/usr/bin/env bash
# Data left behind when a worktree or state dir goes away, and how a user can reclaim it.
# Run from the repository root.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()
git worktree add -q "$RUNNER_TEMP/wt" HEAD
WT=$RUNNER_TEMP/wt/examples/greeting

step "O1 up --name w in a worktree, then stop it"
cd "$WT"
disk before
start_up "$L/w.log" up --name w
wait_log "$L/w.log" " is up\." 1200 || { tail_state w; exit 1; }
disk "w up"
kill -INT -- -$PID; wait_exit $PID 120
obs "teardown output:"; tail -3 "$L/w.log"
disk "w stopped"
used_stopped=$(docker exec "$(engine)" du -sm /var/lib/dagger | cut -f1)

step "O2 remove the worktree, as git worktree remove or git clean -fdX would"
cd "$GITHUB_WORKSPACE"
git worktree remove --force "$RUNNER_TEMP/wt"
disk "worktree removed"
obs "status from the main checkout: $(cd examples/greeting && /tmp/devenv status | tr '\n' ' ')"
obs "down --purge --name w from the main checkout: $(cd examples/greeting && /tmp/devenv down --purge --name w 2>&1 | tail -1)"

step "O3 engine GC policy and dagger's own prune"
bin=$(ls ~/.cache/dagger/dagger-* | head -1)
for f in max-used-space min-free-space reserved-space target-space; do obs "engine $f: $(DAGGER_NO_NAG=1 timeout 120 "$bin" -s core engine local-cache $f 2>&1 | tail -1)"; done
t=$SECONDS; DAGGER_NO_NAG=1 timeout 600 "$bin" -s core engine local-cache prune 2>&1 | tail -2
obs "dagger local-cache prune took $((SECONDS - t))s"
disk "after dagger prune"
used_pruned=$(docker exec "$(engine)" du -sm /var/lib/dagger | cut -f1)
summary+=("engine MB with stopped orphan: $used_stopped; after dagger prune: $used_pruned")

step "O4 recreate the worktree's state dir and purge"
mkdir -p "$RUNNER_TEMP/wt/examples/greeting/.devenv/w"
t=$SECONDS; (cd "$RUNNER_TEMP/wt/examples/greeting" && /tmp/devenv down --purge --name w 2>&1 | tail -2)
obs "purge from a recreated state dir took $((SECONDS - t))s"
disk "after purge from recreated dir"
summary+=("engine MB after purge from recreated dir: $(docker exec "$(engine)" du -sm /var/lib/dagger | cut -f1)")

step "O5 a random-named test that fails, and its cleanup hint"
cd "$GITHUB_WORKSPACE/examples/greeting"
start_up "$L/t.log" test
# Fail the test by stopping it once the workload cluster stage starts.
if wait_log "$L/t.log" "consumer components" 1200; then
  R=$(ls .devenv | head -1)
  /tmp/devenv down --name "$R" >/dev/null 2>&1; wait_exit $PID 300
  obs "failed random test output tail:"; tail -6 "$L/t.log"
  disk "random $R failed"
  obs "status: $(/tmp/devenv status | tr '\n' ' ')"
  t=$SECONDS; /tmp/devenv down --purge --name "$R"; obs "purge $R: rc=$? in $((SECONDS - t))s"
  disk "random $R purged"
fi

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
