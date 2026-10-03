#!/usr/bin/env bash
# Shared helpers for the newcomer exploration scripts.
T0=$SECONDS
obs() { echo "OBS [$((SECONDS - T0))s]: $*" >&2; }
SUMMARY=()
note() { obs "$*"; SUMMARY+=("$*"); }
summary() {
  echo "================ SUMMARY ================" >&2
  printf 'SUM: %s\n' "${SUMMARY[@]}" >&2
}
# timed LABEL CMD...: runs CMD, prints its output and how long it took.
timed() {
  local label=$1; shift
  local s=$SECONDS
  "$@"; local rc=$?
  note "$label: exit $rc in $((SECONDS - s))s"
  return $rc
}
# timed_tail LABEL N CMD...: like timed, but prints only the last N lines of combined output.
timed_tail() {
  local label=$1 n=$2; shift 2
  local s=$SECONDS out
  out=$(mktemp)
  "$@" >"$out" 2>&1; local rc=$?
  tail -"$n" "$out"
  note "$label: exit $rc in $((SECONDS - s))s"
  return $rc
}
# start_up LOG CMD...: starts an up command in its own process group, as a terminal would. Sets UP_PID.
start_up() {
  local log=$1; shift
  setsid "$@" >"$log" 2>&1 &
  UP_PID=$!
  UP_START=$SECONDS
}
# wait_up LOG TIMEOUT: waits for "is up." Returns 1 if up exited or timed out.
wait_up() {
  local log=$1 timeout=$2 end=$((SECONDS + $2))
  while [ $SECONDS -lt $end ]; do
    if grep -q " is up\." "$log"; then note "up reached 'is up.' after $((SECONDS - UP_START))s"; return 0; fi
    if ! kill -0 "$UP_PID" 2>/dev/null; then note "up exited early after $((SECONDS - UP_START))s"; tail -40 "$log"; return 1; fi
    sleep 5
  done
  note "up timed out after $timeout s"; tail -40 "$log"; return 1
}
# ctrl_c: sends SIGINT to up's process group, as Ctrl-C in its terminal would, and waits for it to exit.
ctrl_c() {
  local s=$SECONDS
  kill -INT -- "-$UP_PID" 2>/dev/null
  local end=$((SECONDS + 300))
  while kill -0 "$UP_PID" 2>/dev/null && [ $SECONDS -lt $end ]; do sleep 1; done
  if kill -0 "$UP_PID" 2>/dev/null; then note "up still running 300s after Ctrl-C"; return 1; fi
  wait "$UP_PID"; local rc=$?
  note "up exited $rc, $((SECONDS - s))s after Ctrl-C"
}
ensure_kubectl() {
  if ! command -v kubectl >/dev/null; then
    curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/v1.34.1/bin/linux/amd64/kubectl" 2>/dev/null ||
      sudo curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/v1.34.1/bin/linux/amd64/kubectl"
    sudo chmod +x /usr/local/bin/kubectl
  fi
  obs "kubectl: $(kubectl version --client 2>&1 | head -1)"
}
dump_state() {
  local dir=$1
  echo "--- files under $dir"; find "$dir" -maxdepth 3 | sort | head -50
  for f in "$dir"/*.log; do [ -f "$f" ] && { echo "--- tail $f ($(wc -l <"$f") lines)"; tail -30 "$f"; }; done
}
