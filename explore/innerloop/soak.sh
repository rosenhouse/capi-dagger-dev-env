#!/usr/bin/env bash
# A day of edit cycles on one environment, then the Dagger engine restarting under it.
source "$(dirname "$0")/common.sh"
setup_greeting
up_bg
cd "$G"
greeting "hello one"
obs "hello serves after $(wait_hello 'hello one' 300)s"
engine=$(docker ps --filter name=dagger-engine --format '{{.Names}}' | head -1)
engmem() { docker stats --no-stream --format '{{.MemUsage}}' "$engine" 2>&1; }
obs "engine $engine mem after up: $(engmem); disk used: $(df --output=used -BG / | tail -1)"

section "S1: repeated edit cycles of hello"
for i in $(seq 1 12); do
  CYC=cycle$i perl -pi -e 's/\(hello %s\)[^"]*"/(hello %s) $ENV{CYC}\\n"/' cmd/hello/main.go
  t=$SECONDS
  rd "cycle$i" >/dev/null
  rt=$((SECONDS - t))
  sv=$(wait_hello "cycle$i" 300)
  obs "cycle$i: redeploy ${rt}s, hello serves ${sv}s later; engine mem $(engmem); disk $(df --output=used -BG / | tail -1)"
done
sed 's/^/    | /' "$W/rd-cycle12.log"
mk get apps -A 2>&1
mk -n kapp-controller get pods 2>&1

section "S2: the Dagger engine restarts under a running environment"
docker restart "$engine" >/dev/null; obs "restarted engine $engine"
sleep 20
obs "up process alive: $(kill -0 "$UP_PID" 2>/dev/null && echo yes || echo no)"
obs "status: $("$DEVENV" status 2>&1 | tail -1)"
obs "kubectl mgmt: $(mk get nodes --request-timeout=10s 2>&1 | tail -1)"
t=$SECONDS; rd after-engine-restart; obs "redeploy after engine restart returned in $((SECONDS - t))s"
tail -20 "$W/up.log"
t=$SECONDS
timeout 360 "$DEVENV" down --name "$NAME" 2>&1; obs "down after engine restart rc=$? in $((SECONDS - t))s"
obs "up process alive after down: $(kill -0 "$UP_PID" 2>/dev/null && echo yes || echo no)"
kill -0 "$UP_PID" 2>/dev/null && { kill -INT "$UP_PID"; sleep 30; obs "up alive after SIGINT: $(kill -0 "$UP_PID" 2>/dev/null && echo yes || echo no)"; }

section "S3: up again with the same name"
t=$SECONDS
if UP_PID=$("$LIB/up-bg.sh" "$W/up2.log" 1500 -- "$DEVENV" up --name "$NAME"); then
  obs "second up took $((SECONDS - t))s"
  "$DEVENV" down --name "$NAME"
else
  obs "second up FAILED after $((SECONDS - t))s"
  tail -30 "$W/up2.log"
fi
summary
