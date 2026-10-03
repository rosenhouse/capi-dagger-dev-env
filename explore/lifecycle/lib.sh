#!/usr/bin/env bash
# Shared helpers for lifecycle exploration. Source it.
T0=$SECONDS
obs() { echo "OBS[$((SECONDS - T0))s]: $*"; }
step() { echo; echo "=== [$((SECONDS - T0))s] $*"; }
engine() { docker ps --filter name=dagger-engine --format '{{.Names}}' | head -1; }

# start_up LOG ARGS... starts devenv in its own session, as a terminal would, and sets PID.
start_up() {
  local log=$1; shift
  setsid /tmp/devenv "$@" >"$log" 2>&1 < /dev/null &
  PID=$!
  obs "started devenv $* as pid $PID (pgid $(ps -o pgid= -p $PID 2>/dev/null | tr -d ' '))"
}

# wait_log LOG PATTERN TIMEOUT waits for PATTERN in LOG while PID lives. Returns 1 on exit or timeout.
wait_log() {
  local log=$1 pat=$2 timeout=$3 t=$SECONDS
  while [ $((SECONDS - t)) -lt "$timeout" ]; do
    if grep -q -- "$pat" "$log"; then obs "'$pat' after $((SECONDS - t))s"; return 0; fi
    if ! kill -0 "$PID" 2>/dev/null; then obs "pid $PID exited before '$pat' after $((SECONDS - t))s"; tail -15 "$log"; return 1; fi
    sleep 3
  done
  obs "timed out waiting for '$pat' after ${timeout}s"; tail -15 "$log"; return 1
}

# wait_exit PID TIMEOUT waits for a process to exit and reports how long it took.
wait_exit() {
  local pid=$1 timeout=$2 t=$SECONDS
  while kill -0 "$pid" 2>/dev/null; do
    if [ $((SECONDS - t)) -ge "$timeout" ]; then obs "pid $pid still alive after ${timeout}s"; return 1; fi
    sleep 1
  done
  obs "pid $pid exited after $((SECONDS - t))s"
}

ports() { # ports NAME [STATE_DIR]
  local d=${2:-.devenv}/$1
  for k in mgmt workload; do
    [ -f "$d/$k.kubeconfig" ] && echo "$k=$(grep -o 'server: .*' "$d/$k.kubeconfig" | sed 's/.*://')"
  done | tr '\n' ' '
}

kc() { # kc KUBECONFIG ARGS...
  local k=$1; shift
  timeout 30 kubectl --kubeconfig "$k" --request-timeout=10s "$@" 2>&1 | tail -3
}

disk() { # disk LABEL
  local e; e=$(engine)
  obs "disk[$1]: host used $(df --output=used -BG / | tail -1 | tr -d ' ') avail $(df --output=avail -BG / | tail -1 | tr -d ' ') engine /var/lib/dagger $( [ -n "$e" ] && docker exec "$e" du -sh /var/lib/dagger 2>/dev/null | cut -f1)"
}

cachevols() { # lists Dagger cache-mount entries and their sizes, as a user could with the dagger CLI
  local bin; bin=$(ls ~/.cache/dagger/dagger-* 2>/dev/null | head -1)
  [ -n "$bin" ] || { obs "no dagger CLI in ~/.cache/dagger"; return; }
  echo '{ engine { localCache { entrySet { diskSpaceBytes entryCount entries { description diskSpaceBytes recordType activelyUsed } } } } }' > /tmp/cache.graphql
  DAGGER_NO_NAG=1 timeout 300 "$bin" query -M -s --doc /tmp/cache.graphql > /tmp/cache.json 2>/tmp/cache.err || { obs "dagger query failed: $(tail -2 /tmp/cache.err)"; return; }
  python3 - "$1" <<'EOF'
import json, sys
d = json.load(open('/tmp/cache.json'))['engine']['localCache']['entrySet']
print(f"OBS: cache[{sys.argv[1]}]: total {d['diskSpaceBytes']/1e9:.2f} GB in {d['entryCount']} entries")
ms = [e for e in d['entries'] if 'cachemount' in (e['recordType'] or '') or 'cache' in (e['description'] or '').lower()]
for e in sorted(ms, key=lambda e: -e['diskSpaceBytes'])[:15]:
    print(f"OBS:   {e['diskSpaceBytes']/1e9:6.2f} GB used={e['activelyUsed']} {e['recordType']} {e['description'][:110]}")
EOF
}

sessions() { # counts processes of environments inside the engine, and Dagger session processes on the host
  local e; e=$(engine)
  docker exec "$e" sh -c 'for p in /proc/[0-9]*; do tr "\0" " " < $p/cmdline 2>/dev/null; echo; done' > /tmp/procs.txt 2>/dev/null
  echo "dockerd=$(grep -c '^dockerd' /tmp/procs.txt) apiservers=$(grep -c '^kube-apiserver' /tmp/procs.txt) registries=$(grep -c 'registry serve' /tmp/procs.txt) host-sessions=$(pgrep -fc 'dagger-0.* session' || true)"
}

status() { /tmp/devenv status "$@" 2>&1 | sed 's/^/    /'; }

tail_state() { # tail_state NAME
  for f in .devenv/"$1"/dagger.log; do [ -f "$f" ] && { echo "--- tail $f"; tail -20 "$f"; }; done
}
