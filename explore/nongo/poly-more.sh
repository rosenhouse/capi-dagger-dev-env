#!/usr/bin/env bash
# Job A2: a patched third-party image that a package forgets to lock, a hook change during redeploy,
# and up with a broken Dockerfile, a panicking hook, and a nil image.
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=$HERE/../lib
T=${RUNNER_TEMP:-/tmp}/polyA2
NAME=more
STATE=$T/poly/.devenv
DEVENV=$T/devenv
SUMMARY=()
. "$HERE/lib.sh"
export GOWORK=off
mkdir -p "$T"
"$LIB/adopter.sh" "$HERE/fixtures/poly" "$T/poly" >/dev/null 2>&1 || obs "adopter.sh failed"
cd "$T/poly" || exit 1
go build -o "$DEVENV" ./cmd/devenv || { obs "devenv main failed to build"; summary; exit 1; }

# 1. The web package's config says image: nginx, and the hook builds a patched "nginx", but the package locks no images.
t=$SECONDS
pid=$(POLY_WEB=1 POLY_WEB_IMAGES="" "$LIB/up-bg.sh" "$T/up.log" 1500 -- "$DEVENV" up --name "$NAME")
if [ -z "$pid" ]; then
  obs "up with web package failed after $((SECONDS - t))s: $(grep -m1 -A3 '^Error' "$T/up.log" | tr '\n' ' ' | cut -c1-600)"
  tail -40 "$T/up.log"
else
  obs "up with web package took $((SECONDS - t))s"
  obs "dagger.log has $(wc -l <"$STATE/$NAME/dagger.log") lines; lines naming the greeter's RUN step: $(grep -c deps-stamp "$STATE/$NAME/dagger.log"); lines naming nginx: $(grep -ci nginx "$STATE/$NAME/dagger.log")"
  head -20 "$STATE/$NAME/dagger.log" | cut -c1-200
  obs "web images: $(kmgmt -n web get deploy web -o jsonpath='{.spec.template.spec.containers[*].image}')"
  obs "web serves: $(kmgmt -n web exec deploy/web -c sidecar -- wget -qO- localhost:80 2>&1 | head -4 | tr '\n' ' ')"
  obs "greeter HOOK_REV before hook change: $(kmgmt -n greeter exec deploy/greeter -- printenv HOOK_REV 2>&1)"

  # 2. Change the hook, rebuild the CLI, and redeploy with the new binary.
  sed -i 's/const hookRev = "1"/const hookRev = "2"/' cmd/devenv/main.go
  go build -o "$T/devenv2" ./cmd/devenv
  t=$SECONDS
  "$T/devenv2" redeploy --name "$NAME" >"$T/rd.out" 2>&1
  obs "redeploy with a rebuilt CLI whose hook changed: rc=$? $((SECONDS - t))s; output: $(tr '\n' ' ' <"$T/rd.out")"
  "$LIB/pause.sh" 15
  obs "greeter HOOK_REV after redeploy: $(kmgmt -n greeter exec deploy/greeter -- printenv HOOK_REV 2>&1)"
  obs "hook revisions the up process ran: $(grep -o 'hook rev [0-9]*' "$T/up.log" | sort | uniq -c | tr '\n' ' ')"
  "$DEVENV" down --name "$NAME"
  for _ in $(seq 60); do kill -0 "$pid" 2>/dev/null || break; sleep 2; done
fi
git checkout -q cmd/devenv/main.go

# 3. up with a Dockerfile that fails to build.
cp services/greeter/Dockerfile "$T/Dockerfile.good"
printf 'RUN python -c "import flask_not_installed"\n' >>services/greeter/Dockerfile
t=$SECONDS
timeout 900 "$DEVENV" up -v --name broken >"$T/broken.log" 2>&1
obs "up -v with a failing Dockerfile: rc=$? after $((SECONDS - t))s; error is $(grep -A100 '^Error' "$T/broken.log" | wc -c) bytes; output lines naming the module: $(grep -c flask_not_installed "$T/broken.log"); dagger.log lines naming it: $(grep -c flask_not_installed "$STATE/broken/dagger.log")"
cat "$T/broken.log" | cut -c1-400 | tail -30
ls "$STATE/broken" "$STATE/broken/logs" 2>&1 | head -20
cp "$T/Dockerfile.good" services/greeter/Dockerfile

# 4. up with a hook that panics.
touch PANIC_HOOK
t=$SECONDS
timeout 900 "$DEVENV" up --name panicky >"$T/panic.log" 2>&1
obs "up with a panicking hook: rc=$? after $((SECONDS - t))s; first lines: $(head -c 300 "$T/panic.log" | tr '\n' ' ')"
grep -m3 -E '^(panic|goroutine)' "$T/panic.log"
obs "status after the panic: $("$DEVENV" status 2>&1 | grep panicky)"
rm -f PANIC_HOOK

# 5. up with a hook that returns a nil image.
t=$SECONDS
POLY_NIL_IMAGE=1 timeout 900 "$DEVENV" up --name nilimage >"$T/nil.log" 2>&1
obs "up with a nil image from the hook: rc=$? after $((SECONDS - t))s; $(grep -m2 -E '^(panic|Error)' "$T/nil.log" | tr '\n' ' ' | cut -c1-300)"
summary
