#!/usr/bin/env bash
# A full disk during bring-up, recovery, the same name in two worktrees, and commands from a subdirectory.
# Run from the repository root.
source "$(dirname "$0")/lib.sh"
L=$RUNNER_TEMP/logs; mkdir -p "$L"
summary=()
WT1=$GITHUB_WORKSPACE/examples/greeting
git worktree add -q "$RUNNER_TEMP/wt2" HEAD
WT2=$RUNNER_TEMP/wt2/examples/greeting
cd "$WT1"

step "D1 up with about ${FREE_GB:=6} GB free"
disk before
avail=$(df --output=avail -B1G / | tail -1 | tr -d ' ')
sudo fallocate -l $((avail - FREE_GB))G /fill && obs "filled the disk; $(df -h / | tail -1)"
start_up "$L/full.log" up --name full
t=$SECONDS
(while kill -0 $PID 2>/dev/null; do df --output=avail -BM / | tail -1; sleep 5; done > "$L/df.log") &
if wait_log "$L/full.log" " is up\." 900; then
  obs "up succeeded with a nearly full disk after $((SECONDS - t))s"; summary+=("up with ${FREE_GB}G free: succeeded")
  kill -INT -- -$PID; wait_exit $PID 120
else
  if kill -0 $PID 2>/dev/null; then
    obs "up still running after the timeout, at: $(grep -o '\] .*' "$L/full.log" | tail -1); sending Ctrl-C"
    kill -INT -- -$PID; wait_exit $PID 180 || kill -9 -- -$PID
  fi
  obs "up with ${FREE_GB}G free failed after $((SECONDS - t))s; min free $(sort -n "$L/df.log" | head -1)"
  obs "output tail:"; tail -25 "$L/full.log"
  tail_state full
  summary+=("up with ${FREE_GB}G free: failed after $((SECONDS - t))s: $(grep -m1 '^Error' "$L/full.log")")
fi
obs "status: $(/tmp/devenv status | grep '^full ')"
obs "engine container: $(docker ps -a --filter name=dagger-engine --format '{{.Names}} {{.Status}}')"
sudo rm -f /fill; disk "fill removed"

step "D2 the same name in two worktrees at once, after the disk filled"
start_up "$L/wt1.log" up --name full; P1=$PID
cd "$WT2"; start_up "$L/wt2.log" up --name full; P2=$PID; cd "$WT1"
PID=$P1; wait_log "$L/wt1.log" " is up\." 1200 && summary+=("worktree 1 recovered after the full disk") || { summary+=("worktree 1 up FAILED after the full disk"); tail_state full; }
PID=$P2; wait_log "$L/wt2.log" " is up\." 900 && summary+=("worktree 2 up") || { summary+=("worktree 2 up FAILED"); (cd "$WT2" && tail_state full); }
obs "ports wt1: $(ports full) wt2: $(cd "$WT2" && ports full)"
disk "two worktree envs up"
obs "contexts when both mgmt kubeconfigs are merged:"
KUBECONFIG=$WT1/.devenv/full/mgmt.kubeconfig:$WT2/.devenv/full/mgmt.kubeconfig kubectl config get-contexts 2>&1 | sed 's/^/    /'
kc "$WT1/.devenv/full/mgmt.kubeconfig" get nodes

step "D3 commands from a subdirectory of the module while an environment runs"
cd "$WT1/cmd"
obs "status from cmd/: $(/tmp/devenv status 2>&1 | tr '\n' ' ')"
obs "redeploy from cmd/: $(/tmp/devenv redeploy 2>&1 | tail -1)"
obs "kubeconfig from cmd/: $(/tmp/devenv kubeconfig 2>&1 | tail -1)"
obs "down from cmd/: $(/tmp/devenv down 2>&1 | tail -1)"
cd "$WT1"

step "D4 a relative --state-dir from another directory"
obs "status --state-dir ../.devenv from cmd/: $(cd cmd && /tmp/devenv status --state-dir ../.devenv 2>&1 | tr '\n' ' ')"
obs "redeploy --state-dir ../.devenv from cmd/:"; (cd cmd && timeout 600 /tmp/devenv redeploy --state-dir ../.devenv 2>&1 | tail -3)

step "D5 tear down both"
kill -INT -- -$P1; kill -INT -- -$P2; wait_exit $P1 120; wait_exit $P2 120
/tmp/devenv down --purge --name full; (cd "$WT2" && /tmp/devenv down --purge --name full)
disk "both purged"

step "SUMMARY"
printf 'OBS: %s\n' "${summary[@]}"
