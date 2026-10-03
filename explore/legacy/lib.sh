# Helpers for the legacy-adopter exploration. Source from a job script.
TOOL=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
LEG=$TOOL/explore/legacy
export GOWORK=off
export GIT_AUTHOR_NAME=x GIT_AUTHOR_EMAIL=x@example.com GIT_COMMITTER_NAME=x GIT_COMMITTER_EMAIL=x@example.com
SUMMARY=()

obs() { echo "OBS: $*"; SUMMARY+=("$*"); }

summary() {
  kill "$HEARTBEAT" 2>/dev/null
  echo "==================== SUMMARY ===================="
  for s in "${SUMMARY[@]}"; do echo "- $s"; done
}

# copy_repo DEST copies the legacy repo and its sibling module to DEST/{repo,common} and commits.
copy_repo() {
  local dest=$1
  rm -rf "$dest"; mkdir -p "$dest"
  cp -a "$LEG/repo" "$dest/repo"; cp -a "$LEG/common" "$dest/common"
  (cd "$dest/repo" && git init -q && git add -A && git commit -qm init)
}

# vendor_repo DIR vendors the module in DIR and commits, as the team does for its sibling replace.
vendor_repo() {
  (cd "$1" && go mod vendor && git add -A && git commit -qm vendor) || obs "go mod vendor failed in $1"
}

# devenv_module DEST makes DEST/repo/hack/devenv its own module that requires this checkout of the tool,
# and builds it to DEST/devenv.
devenv_module() {
  local dest=$1
  (cd "$dest/repo/hack/devenv" &&
    go mod init github.com/acme/fleet-addons/hack/devenv >/dev/null 2>&1 &&
    go mod edit -require=github.com/rosenhouse/capi-dagger-dev-env@v0.0.0-00010101000000-000000000000 \
      -replace=github.com/rosenhouse/capi-dagger-dev-env="$TOOL" &&
    go mod tidy && go build -o "$dest/devenv" . &&
    cd "$dest/repo" && git add -A && git commit -qm devenv) || obs "building the nested devenv module failed"
}

# render DIR KUSTOMIZATION OUT renders a kustomization in the repo at DIR.
render() {
  (cd "$1" && kubectl kustomize "$2" > "$3" && git add -A && git commit -qm render) || obs "kustomize render of $2 failed"
}

# expect_fail LOG TIMEOUT -- CMD runs an up that should fail and reports how and when.
# It stops an up that comes up or outlives TIMEOUT.
expect_fail() {
  local log=$1 timeout=$2; shift 3
  local start=$SECONDS
  "$@" >"$log" 2>&1 &
  local pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    if grep -q " is up\." "$log"; then
      obs "UNEXPECTED: $* came up after $((SECONDS - start))s"
      stop_up "$pid"; return 0
    fi
    if [ $((SECONDS - start)) -gt "$timeout" ]; then
      obs "TIMEOUT: $* still running after ${timeout}s; last line: $(tail -1 "$log")"
      stop_up "$pid"; tail -25 "$log"; return 1
    fi
    sleep 5
  done
  echo "---- tail of $log"; tail -25 "$log"
  obs "failed after $((SECONDS - start))s: $(grep -m1 -E '^Error:' "$log" | cut -c1-300)"
  return 1
}

# heartbeat prints memory and the newest progress line every minute, so a hung job shows where it is.
heartbeat() {
  while sleep 60; do
    echo "HEARTBEAT $(date +%T) mem_used=$(free -m | awk '/Mem/{print $3}')MB $(ls -t "$RUNNER_TEMP"/*.log 2>/dev/null | head -1 | xargs -r tail -1 | cut -c1-160)"
  done
}
heartbeat &
HEARTBEAT=$!

# stop_up PID interrupts an up process, as Ctrl-C does, and waits for it to exit.
stop_up() {
  local start=$SECONDS
  kill -INT "$1" 2>/dev/null
  while kill -0 "$1" 2>/dev/null && [ $((SECONDS - start)) -lt 180 ]; do sleep 2; done
  kill -9 "$1" 2>/dev/null
  echo "up $1 stopped after $((SECONDS - start))s"
}

# state_tail DIR NAME prints what an adopter would look at after a failure.
state_tail() {
  local d=$1/.devenv/$2
  echo "---- $d"; ls -la "$d" 2>/dev/null
  [ -f "$d/logs/resources.yaml" ] && grep -nE 'usefulErrorMessage|friendlyDescription' -A3 "$d/logs/resources.yaml" | head -40
  [ -f "$d/dagger.log" ] && grep -E 'ERROR|error' "$d/dagger.log" | tail -15
}
