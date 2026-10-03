#!/usr/bin/env bash
# Job B: a polyglot repo with no go.mod at its root. The devenv main is its own module in tools/devenv,
# Config.Root is "../..", Commands is empty, and the only image is a Dockerfile-built Python greeter.
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=$HERE/../lib
TOOL=$(cd "$HERE/../.." && pwd)
T=${RUNNER_TEMP:-/tmp}/nestedB
R=$T/nested
NAME=nested
STATE=$R/tools/devenv/.devenv
DEVENV=$T/devenv
SUMMARY=()
. "$HERE/lib.sh"
export GOWORK=off
mkdir -p "$R"

cp -a "$HERE/fixtures/nested/." "$R/"
mkdir -p "$R/services" "$R/config"
cp -a "$HERE/fixtures/poly/services/greeter" "$R/services/"
cp -a "$HERE/fixtures/poly/config/greeter" "$R/config/"
cd "$R" && git init -q && git add -A && git -c user.email=x@x -c user.name=x commit -qm init
(cd tools/devenv && go mod edit -require=github.com/rosenhouse/capi-dagger-dev-env@v0.0.0-00010101000000-000000000000 \
  -replace=github.com/rosenhouse/capi-dagger-dev-env="$TOOL" && go mod tidy) >/dev/null 2>&1 || obs "go mod tidy failed"

# How would a user run it from the repo root?
out=$(go run ./tools/devenv status 2>&1); obs "from repo root, 'go run ./tools/devenv status' -> rc=$? : $(echo "$out" | head -2 | tr '\n' ' ')"
go -C tools/devenv build -o "$DEVENV" . || { obs "devenv main failed to build"; summary; exit 1; }

# Root left empty.
out=$(cd "$R" && NESTED_ROOT= timeout 120 "$DEVENV" up --name n0 2>&1); obs "Root empty, run from repo root: rc=$? : $(echo "$out" | tail -3 | tr '\n' ' ')"
out=$(cd "$R/tools/devenv" && NESTED_ROOT= timeout 120 "$DEVENV" up --name n0 2>&1); obs "Root empty, run from tools/devenv: rc=$? : $(echo "$out" | tail -3 | tr '\n' ' ')"
# Root "../.." but run from the repo root.
out=$(cd "$R" && timeout 120 "$DEVENV" up --name n0 2>&1); obs "Root ../.., run from repo root: rc=$? : $(echo "$out" | tail -3 | tr '\n' ' ')"

cd "$R/tools/devenv" || exit 1
t=$SECONDS
pid=$("$LIB/up-bg.sh" "$T/up.log" 1500 -- "$DEVENV" up --name "$NAME")
if [ -z "$pid" ]; then obs "up failed after $((SECONDS - t))s"; tail -60 "$T/up.log"; dump_state; summary; exit 1; fi
obs "cold up (Commands empty, Root ../..) took $((SECONDS - t))s"
grep -E '^\[' "$T/up.log"
grep OBS-HOOK "$T/up.log"
g0=$(img greeter greeter)

redeploy
g1=$(img greeter greeter)
obs "no-op redeploy #1 rc=$RD_RC ${RD_SECS}s; greeter image $(same "$g0" "$g1")"
redeploy
g2=$(img greeter greeter)
obs "no-op redeploy #2 rc=$RD_RC ${RD_SECS}s; greeter image $(same "$g1" "$g2")"
echo "--- hook lines:"; grep OBS-HOOK "$T/up.log" | cut -c1-600
kmgmt -n greeter logs deploy/greeter --tail=2

sed -i 's/hello-v1/hello-v2/' "$R/services/greeter/app.py"
redeploy
g3=$(img greeter greeter)
obs "python edit redeploy rc=$RD_RC ${RD_SECS}s; greeter image $(same "$g2" "$g3")"
kmgmt -n greeter logs deploy/greeter --tail=2

obs "status from tools/devenv: $("$DEVENV" status 2>&1 | tail -1)"
obs "status from repo root: $(cd "$R" && "$DEVENV" status 2>&1 | tail -1)"
obs "git status in repo root shows: $(cd "$R" && git status --porcelain | tr '\n' ' ')"

"$DEVENV" down --name "$NAME" --purge
for _ in $(seq 60); do kill -0 "$pid" 2>/dev/null || break; sleep 2; done
summary
