#!/usr/bin/env bash
# Usage: run.sh JOB. Injects failures into a small consumer and reports what devenv says and keeps.
REPO=$(cd "$(dirname "$0")/../.." && pwd)
. "$REPO/explore/diagnosis/lib.sh"
PAUSE=$REPO/explore/lib/pause.sh
JOB_START=$SECONDS
left() { echo $((40 * 60 - (SECONDS - JOB_START))); }

manifests() {
  run_case M1-manifests 1500 DIAG_SCENARIO=manifests -- test --name man
  inspect M1-manifests man
  grep_logs M1-manifests man 'flag provided but not defined' "crash: container's own error"
  grep_logs M1-manifests man 'helo' "typo: image name helo"
  grep_logs M1-manifests man 'ServiceMonitor' "nocrd: ServiceMonitor kind"
  grep_logs M1-manifests man 'Readiness probe failed' "probe: readiness probe failures"
  grep_logs M1-manifests man 'serviceaccount "hello" not found' "nsmismatch: missing service account"
  echo "----- crash pod log files"; find "$SRC/.devenv/man/logs" -path '*crash*' | head; echo "-----"

  run_case M2-rbac-v 900 DIAG_SCENARIO=rbac -- test --name man -v
  inspect M2-rbac-v man
  grep_logs M2-rbac-v man 'configmaps is forbidden' "rbac: controller's forbidden error"
  grep_logs M2-rbac-v man 'flag provided but not defined' "stale: M1's crash log still present"
  obs "[M2-rbac-v] lines containing 'level=' or 'Creating new Engine': $(grep -cE 'level=|Engine' "$RUNNER_TEMP/out-M2-rbac-v.log")"

  if [ "$(left)" -gt 900 ]; then
    local t=$SECONDS
    pid=$(cd "$SRC" && DIAG_SCENARIO=good "$REPO/explore/lib/up-bg.sh" "$RUNNER_TEMP/out-M3-up.log" 900 -- "$DIAG" up --name man)
    obs "[M3-up] up pid=${pid:-none} after $((SECONDS - t))s"
    if [ -n "$pid" ]; then
      sed -i 's/        image: hello/        image: hello\n        args: ["--leader-elect"]/' "$SRC/config/good/good.yaml"
      t=$SECONDS
      (cd "$SRC" && timeout 600 "$DIAG" redeploy --name man) >"$RUNNER_TEMP/out-M3-redeploy.log" 2>&1
      obs "[M3-redeploy crashloop] exit=$? after $((SECONDS - t))s"
      tail -20 "$RUNNER_TEMP/out-M3-redeploy.log"
      (cd "$SRC" && git checkout -q config/good/good.yaml)
      (cd "$SRC" && timeout 300 "$DIAG" down --name man) 2>&1 | tail -5
      obs "[M3] up exit tail: $(tail -3 "$RUNNER_TEMP/out-M3-up.log" | tr '\n' ' ')"
    fi
  fi
}

hooks() {
  run_case H1-ready-fail 1500 DIAG_SCENARIO=ready-fail -- test --name hk
  inspect H1-ready-fail hk
  local ready_at
  ready_at=$(grep -oE '^\[ *[0-9.]+s\] consumer components' "$RUNNER_TEMP/out-H1-ready-fail.log" | grep -oE '[0-9]+' | head -1)
  obs "[H1-ready-fail] consumer components stage began at ${ready_at:-?}s"

  run_case H2-test-fail 1200 DIAG_SCENARIO=test-fail -- test
  local env
  env=$(ls "$SRC/.devenv" | grep '^env-' | head -1)
  obs "[H2-test-fail] random env left behind: ${env:-none}"
  [ -n "$env" ] && inspect H2-test-fail "$env"

  run_case H3-test-panic 1200 DIAG_SCENARIO=test-panic -- test --name hk
  inspect H3-test-panic hk
  obs "[H3-test-panic] status afterwards: $(cd "$SRC" && "$DIAG" status 2>&1 | tr '\n' ' ')"
  obs "[H3-test-panic] sockets: $(ls ~/.cache/devenv 2>&1 | tr '\n' ' ')"

  local deadline=$(( ${ready_at:-500} + 60 ))
  run_case H4-ready-hang-deadline 1500 DIAG_SCENARIO=ready-hang DIAG_DEADLINE=$deadline -- test --name hk
  inspect H4-ready-hang-deadline hk
}

platform() {
  cp "$SRC/cmd/hello/main.go" "$RUNNER_TEMP/hello.go"
  sed -i 's/log.Printf("hello %s listening on %s", version, \*addr)/log.Printf("hello %s listening on %s", version, addr.Port)/' "$SRC/cmd/hello/main.go"
  run_case P1-compile-error 900 DIAG_SCENARIO=good -- test --name plat
  inspect P1-compile-error plat
  cp "$RUNNER_TEMP/hello.go" "$SRC/cmd/hello/main.go"

  run_case P2-dockerfile-fail-v 900 DIAG_SCENARIO=web -- test --name plat -v
  inspect P2-dockerfile-fail-v plat

  run_case P3-no-docker 420 DIAG_SCENARIO=good DOCKER_HOST=unix:///tmp/no-docker.sock -- test --name eng
  inspect P3-no-docker eng

  sudo iptables -I FORWARD 1 -p tcp --dport 443 -m string --string "quay.io" --algo bm -j REJECT --reject-with tcp-reset
  obs "[P4] quay.io from a container: $(docker run --rm curlimages/curl:8.11.1 -sS -m 10 -o /dev/null -w '%{http_code}' https://quay.io/v2/ 2>&1 | tail -1)"
  obs "[P4] ghcr.io from a container: $(docker run --rm curlimages/curl:8.11.1 -sS -m 10 -o /dev/null -w '%{http_code}' https://ghcr.io/v2/ 2>&1 | tail -1)"
  run_case P4-quay-blocked 1200 DIAG_SCENARIO=good -- test --name plat
  inspect P4-quay-blocked plat
  sudo iptables -D FORWARD 1

  local out=$RUNNER_TEMP/out-P5-sigint.log t=$SECONDS
  touch "$RUNNER_TEMP/stamp-P5-sigint"
  (cd "$SRC" && DIAG_SCENARIO=test-hang exec "$DIAG" test --name plat) >"$out" 2>&1 &
  local pid=$!
  while kill -0 $pid 2>/dev/null && ! grep -q 'DIAG: test started' "$out" && [ $((SECONDS - t)) -lt 1200 ]; do sleep 5; done
  obs "[P5-sigint] test hook started after $((SECONDS - t))s"
  sleep 20
  local s=$SECONDS
  kill -INT $pid
  while kill -0 $pid 2>/dev/null && [ $((SECONDS - s)) -lt 300 ]; do sleep 2; done
  if kill -0 $pid 2>/dev/null; then obs "[P5-sigint] still running 300s after SIGINT"; kill -INT $pid; sleep 10; kill -9 $pid; fi
  wait $pid
  obs "[P5-sigint] exit=$? $((SECONDS - s))s after SIGINT"
  tail -25 "$out"
  inspect P5-sigint plat
}

echo "OBS: runner $(uname -m) $(nproc) cpus, $(free -g | awk '/Mem/{print $2}') GB"
setup
"$1"
echo "===== SUMMARY ($1)"
cat "$SUMMARY"
