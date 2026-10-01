# Helpers for exploratory runs. Source from the repo root.
set -uo pipefail
L=${RUNNER_TEMP:-/tmp}/explore
mkdir -p "$L"

say() { echo; echo "=== $*"; }

# run prints a command, its output, exit code and duration, and never fails.
run() {
  local s=$SECONDS
  echo "+ $*"
  "$@"
  echo "[exit $? after $((SECONDS - s))s]"
}

# bg starts a command in its own session and process group, as a terminal's foreground job would be.
# It sets BGPID and PGID. Output goes to $L/<tag>.log.
bg() {
  local tag=$1; shift
  rm -f "$L/$tag.pgid"
  # A script's background jobs ignore SIGINT, so restore it, as a terminal's job has it.
  python3 -c 'import os, signal, sys
signal.signal(signal.SIGINT, signal.SIG_DFL); signal.signal(signal.SIGQUIT, signal.SIG_DFL)
os.setsid(); open(sys.argv[1], "w").write(str(os.getpid())); os.execvp(sys.argv[2], sys.argv[2:])' "$L/$tag.pgid" "$@" >"$L/$tag.log" 2>&1 &
  BGPID=$!
  until [ -s "$L/$tag.pgid" ]; do sleep 0.1; done
  PGID=$(cat "$L/$tag.pgid")
  echo "started $tag: pid $BGPID pgid $PGID: $*"
}

# waitlog waits up to max seconds for a line matching pattern in tag's log.
waitlog() {
  local tag=$1 pattern=$2 max=$3 s=$SECONDS
  until grep -qE -- "$pattern" "$L/$tag.log"; do
    if [ $((SECONDS - s)) -ge "$max" ]; then echo "TIMEOUT waiting for /$pattern/ in $tag"; return 1; fi
    sleep 1
  done
  echo "saw /$pattern/ in $tag after $((SECONDS - s))s"
}

# finish waits for the background job and prints its exit code and log.
finish() {
  local tag=$1 s=$SECONDS
  wait "$BGPID"
  echo "[$tag exit $? ; waited $((SECONDS - s))s]"
  echo "--- $tag log:"; cat "$L/$tag.log"; echo "--- end $tag log"
}

procs() { ps -eo pid,pgid,sid,etimes,rss,args --sort=pid | grep -E 'smolvm|krun|devenv' | grep -vE 'grep|ps -eo' || true; }

machines() { echo "+ smolvm machine ls"; smolvm machine ls; }
