#!/usr/bin/env bash
# Usage: up-bg.sh LOGFILE TIMEOUT_SECONDS -- COMMAND...
# Starts an `up` command in the background, waits until it prints "is up." or exits.
# Prints the PID on success. Exits 1 if up exited or timed out.
log=$1; timeout=$2; shift 3
"$@" >"$log" 2>&1 &
pid=$!
end=$((SECONDS + timeout))
while [ $SECONDS -lt $end ]; do
  if grep -q " is up\." "$log"; then echo "$pid"; exit 0; fi
  if ! kill -0 "$pid" 2>/dev/null; then echo "up exited early:" >&2; tail -40 "$log" >&2; exit 1; fi
  sleep 5
done
echo "up timed out after $timeout s:" >&2; tail -40 "$log" >&2
exit 1
