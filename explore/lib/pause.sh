#!/usr/bin/env bash
# Usage: pause.sh SECONDS. Waits in short steps, for agents whose shells block a bare sleep.
end=$((SECONDS + ${1:-60}))
while [ $SECONDS -lt $end ]; do sleep 5; done
