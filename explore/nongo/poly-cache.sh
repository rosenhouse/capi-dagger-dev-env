#!/usr/bin/env bash
# Job A3: what a Go-only edit does to a Dockerfile image whose hook passes the version, and where build output goes.
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=$HERE/../lib
T=${RUNNER_TEMP:-/tmp}/polyA3
NAME=cache
STATE=$T/poly/.devenv
DEVENV=$T/devenv
SUMMARY=()
. "$HERE/lib.sh"
export GOWORK=off
mkdir -p "$T"
"$LIB/adopter.sh" "$HERE/fixtures/poly" "$T/poly" >/dev/null 2>&1 || obs "adopter.sh failed"
cd "$T/poly" || exit 1
go build -o "$DEVENV" ./cmd/devenv || { obs "devenv main failed to build"; summary; exit 1; }
start_line() { kmgmt -n greeter logs "$(kmgmt -n greeter get pods --sort-by=.metadata.creationTimestamp -o name | tail -1)" | grep -m1 greeting; }

t=$SECONDS
pid=$("$LIB/up-bg.sh" "$T/up.log" 1500 -- "$DEVENV" up -v --name "$NAME")
if [ -z "$pid" ]; then obs "up failed after $((SECONDS - t))s"; tail -60 "$T/up.log"; summary; exit 1; fi
obs "up -v took $((SECONDS - t))s; RUN output 'computed-42' lines: up -v stderr $(grep -c computed-42 "$T/up.log"), dagger.log $(grep -c computed-42 "$STATE/$NAME/dagger.log")"
grep -m3 computed-42 "$T/up.log"
m0=$(img poly manager); g0=$(img greeter greeter); s0=$(start_line)
obs "greeter start: $s0"

# A Go-only edit.
sed -i 's/manager version=/manager v2 version=/' cmd/manager/main.go
redeploy
m1=$(img poly manager); g1=$(img greeter greeter); "$LIB/pause.sh" 10; s1=$(start_line)
obs "go-only edit redeploy rc=$RD_RC ${RD_SECS}s; manager image $(same "$m0" "$m1"), greeter image $(same "$g0" "$g1")"
obs "greeter start after go-only edit: $s1"
obs "greeter pods: $(kmgmt -n greeter get pods --no-headers | awk '{print $1, $3, $5}' | tr '\n' ';')"

"$DEVENV" down --name "$NAME"
for _ in $(seq 60); do kill -0 "$pid" 2>/dev/null || break; sleep 2; done
summary
