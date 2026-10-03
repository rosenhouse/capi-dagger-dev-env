#!/usr/bin/env bash
# README's default flow without --name: Ctrl-C as up suggests, repeat, then clean up; a failing test.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
ensure_kubectl
cd "$here/../../examples/greeting"
vol() { docker system df --format '{{.Type}}={{.Size}}' | grep Volumes; }
D() { go run ./cmd/devenv "$@"; }
D status >/dev/null

for i in 1 2; do
  echo "################ go run ./cmd/devenv up (no --name), then Ctrl-C, round $i"
  start_up "$RUNNER_TEMP/r$i.log" go run ./cmd/devenv up
  wait_up "$RUNNER_TEMP/r$i.log" 1200 || { dump_state .devenv/*; }
  note "round $i engine volumes while up: $(vol)"
  ctrl_c
  tail -3 "$RUNNER_TEMP/r$i.log"
  note "round $i engine volumes after Ctrl-C: $(vol)"
  D status
done
note "state dirs after two Ctrl-C'd random environments: $(ls .devenv | tr '\n' ' ')"
echo "################ clean up as README says"
D down --purge >"$RUNNER_TEMP/p.out" 2>&1; note "down --purge without --name: exit $?: $(tail -1 "$RUNNER_TEMP/p.out")"
D down >"$RUNNER_TEMP/p.out" 2>&1; note "down without --name: exit $?: $(tail -1 "$RUNNER_TEMP/p.out")"
for n in $(ls .devenv); do timed "down --purge --name $n" D down --purge --name "$n"; done
note "engine volumes after purging both: $(vol)"
docker system df -v | sed -n '/Local Volumes/,/Build cache/p' | head -10

echo "################ a failing go run ./cmd/devenv test"
sed -i 's|"%s (hello %s)\\n"|"%s / hello %s\\n"|' cmd/hello/main.go
s=$SECONDS
D test >"$RUNNER_TEMP/t.log" 2>&1; rc=$?
note "failing test: exit $rc in $((SECONDS - s))s"
tail -40 "$RUNNER_TEMP/t.log" | cut -c1-400
name=$(ls .devenv | head -1)
find ".devenv/$name" -maxdepth 3 | head -30
du -sh ".devenv/$name" ".devenv/$name"/* 2>/dev/null
printed=$(sed -n 's/^clean up: //p' "$RUNNER_TEMP/t.log")
note "test prints clean-up command: '$printed'"
bash -c "$printed" >"$RUNNER_TEMP/c.out" 2>&1; note "printed clean-up command exit $?: $(tail -1 "$RUNNER_TEMP/c.out")"
D down --purge --name "$name"
D status
note "engine volumes at end: $(vol)"
summary
