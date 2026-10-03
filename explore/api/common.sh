#!/usr/bin/env bash
# Sourced by the explore/api scripts. Sets up the legacyctl consumer as an adopter repo.
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
export GOWORK=off
C=$RUNNER_TEMP/consumer
SUMMARY=()
obs() { echo "OBS: $*"; SUMMARY+=("$*"); }
summary() {
  echo "===== SUMMARY ====="
  printf '%s\n' "${SUMMARY[@]}"
}
# tails prints the tail of every file in an environment's state dir.
tails() {
  local dir=$1
  echo "--- state dir $dir"
  ls -la "$dir" 2>&1
  ls "$dir/logs" 2>/dev/null | head -20
  for f in "$dir"/*.log; do [ -f "$f" ] && { echo "--- tail $f"; tail -15 "$f" | cut -c1-300; }; done
}
setup() {
  "$repo/explore/lib/adopter.sh" "$here/consumer" "$C" || { echo "adopter failed"; exit 1; }
  cd "$C" || exit 1
  local t=$SECONDS
  go build -o "$RUNNER_TEMP/devenv" ./cmd/devenv && go build -o "$RUNNER_TEMP/lifecycle" ./cmd/lifecycle || { echo "build failed"; exit 1; }
  echo "built consumer CLIs in $((SECONDS - t))s"
}
