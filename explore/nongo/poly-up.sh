#!/usr/bin/env bash
# Job A: a Go manager plus a Dockerfile-built Python greeter, both Management packages.
# Brings the environment up, then redeploys through edits and breakages.
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=$HERE/../lib
T=${RUNNER_TEMP:-/tmp}/polyA
NAME=poly
STATE=$T/poly/.devenv
DEVENV=$T/devenv
SUMMARY=()
. "$HERE/lib.sh"
export GOWORK=off
mkdir -p "$T"

"$LIB/adopter.sh" "$HERE/fixtures/poly" "$T/poly" >/dev/null 2>&1 || obs "adopter.sh failed"
cd "$T/poly" || exit 1
go build -o "$DEVENV" ./cmd/devenv || { obs "devenv main failed to build"; summary; exit 1; }

t=$SECONDS
pid=$("$LIB/up-bg.sh" "$T/up.log" 1500 -- "$DEVENV" up --name "$NAME")
if [ -z "$pid" ]; then obs "up failed after $((SECONDS - t))s"; cat "$T/up.log" | tail -60; dump_state; summary; exit 1; fi
obs "cold up took $((SECONDS - t))s"
grep -E '^\[' "$T/up.log"
obs "hook calls during up: $(grep -c OBS-HOOK "$T/up.log")"

m0=$(img poly manager); g0=$(img greeter greeter)
echo "manager=$m0"; echo "greeter=$g0"
kmgmt -n greeter logs deploy/greeter --tail=2

# 1. No-op redeploy.
redeploy
m1=$(img poly manager); g1=$(img greeter greeter)
obs "no-op redeploy rc=$RD_RC ${RD_SECS}s; manager image $(same "$m0" "$m1"), greeter image $(same "$g0" "$g1")"

# 2. Edit only the Python service.
sed -i 's/hello-v1/hello-v2/' services/greeter/app.py && git commit -qam "py edit"
redeploy
m2=$(img poly manager); g2=$(img greeter greeter)
obs "python-only edit redeploy rc=$RD_RC ${RD_SECS}s; manager image $(same "$m1" "$m2"), greeter image $(same "$g1" "$g2")"
kmgmt -n greeter logs deploy/greeter --tail=2
kmgmt -n poly logs deploy/manager --tail=2
kmgmt -n poly get pods -o wide

# 3. Edit only README, and stamp an explicit version.
echo "more docs" >>README.md
redeploy --version 1.2.3
m3=$(img poly manager); g3=$(img greeter greeter)
obs "README-only edit + --version 1.2.3 rc=$RD_RC ${RD_SECS}s; manager image $(same "$m2" "$m3"), greeter image $(same "$g2" "$g3")"
kmgmt -n greeter logs deploy/greeter --tail=2
grep OBS-HOOK "$T/up.log" | tail -3

# 4. A Dockerfile RUN step that fails.
cp services/greeter/Dockerfile "$T/Dockerfile.good"
printf 'RUN echo installing-deps && python -c "import flask_not_installed"\n' >>services/greeter/Dockerfile
redeploy
obs "failing RUN redeploy rc=$RD_RC ${RD_SECS}s; output mentions ModuleNotFoundError: $(grep -c ModuleNotFoundError "$T/rd.out"); dagger.log mentions it: $(grep -c ModuleNotFoundError "$STATE/$NAME/dagger.log")"
echo "--- dagger.log lines near the failure:"
grep -n -B3 -A3 ModuleNotFoundError "$STATE/$NAME/dagger.log" | tail -25
"$DEVENV" status
kill -0 "$pid" 2>/dev/null && obs "up still alive after failed build" || obs "up DIED after failed build"

# 5. COPY of a git-ignored generated file.
cp "$T/Dockerfile.good" services/greeter/Dockerfile
mkdir -p services/greeter/generated && echo 'STUB = 1' >services/greeter/generated/api_pb2.py
printf 'COPY generated/api_pb2.py /app/api_pb2.py\n' >>services/greeter/Dockerfile
redeploy
obs "COPY of git-ignored file redeploy rc=$RD_RC ${RD_SECS}s"

# 6. A Python service that crashes at start.
cp "$T/Dockerfile.good" services/greeter/Dockerfile
sed -i '1i raise SystemExit("boom: GREETER_CONFIG is not set")' services/greeter/app.py
redeploy
obs "crash-at-start redeploy rc=$RD_RC ${RD_SECS}s; output mentions boom: $(grep -c boom "$T/rd.out"); mentions CrashLoop: $(grep -ci crashloop "$T/rd.out")"
kmgmt -n greeter get pods
kmgmt get apps -A
# Recover.
sed -i '1d' services/greeter/app.py
redeploy
obs "recovery redeploy after crash rc=$RD_RC ${RD_SECS}s"
kmgmt -n greeter get pods

# 7. A hook that panics during redeploy.
touch PANIC_HOOK
redeploy
obs "panicking hook redeploy rc=$RD_RC ${RD_SECS}s: $(tail -1 "$T/rd.out")"
pause() { "$LIB/pause.sh" "$1"; }
pause 10
kill -0 "$pid" 2>/dev/null && obs "up alive after hook panic" || obs "up process DIED after hook panic"
echo "--- up.log tail:"; tail -25 "$T/up.log"
"$DEVENV" status
rm -f PANIC_HOOK

if kill -0 "$pid" 2>/dev/null; then
  "$DEVENV" down --name "$NAME"
  for _ in $(seq 60); do kill -0 "$pid" 2>/dev/null || break; sleep 2; done
fi
summary
