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

wait_gone() { local pid=$1 limit=$2 s=$SECONDS; while kill -0 "$pid" 2>/dev/null && [ $((SECONDS - s)) -lt "$limit" ]; do sleep 2; done; }

crashonly() {
  local t=$SECONDS pid
  pid=$(cd "$SRC" && DIAG_SCENARIO=crashonly "$REPO/explore/lib/up-bg.sh" "$RUNNER_TEMP/out-CO1-up.log" 1200 -- "$DIAG" up --name co)
  obs "[CO1-up] up printed 'is up': ${pid:+yes} after $((SECONDS - t))s"
  grep -v '^\s*$' "$RUNNER_TEMP/out-CO1-up.log" | tail -20
  if [ -n "$pid" ]; then
    local k=$SRC/.devenv/co/mgmt.kubeconfig
    obs "[CO1-up] PackageInstalls: $(kubectl --kubeconfig "$k" get pkgi -n devenv --no-headers 2>&1 | tr -s ' ' | tr '\n' ';')"
    obs "[CO1-up] crash pods: $(kubectl --kubeconfig "$k" get pods -n crash --no-headers 2>&1 | tr -s ' ' | tr '\n' ';')"
    "$PAUSE" 60
    obs "[CO1-up] crash pods 60s later: $(kubectl --kubeconfig "$k" get pods -n crash --no-headers 2>&1 | tr -s ' ' | tr '\n' ';')"
    kubectl --kubeconfig "$k" get events -n crash 2>&1 | tail -6
    (cd "$SRC" && timeout 300 "$DIAG" down --name co) 2>&1 | tail -3
    wait_gone "$pid" 120
  fi
  run_case CO2-test 1200 DIAG_SCENARIO=crashonly -- test --name co
  run_case CO3-test 1200 DIAG_SCENARIO=crashonly -- test --name co
}

upstream() {
  run_case UB1-good 1500 DIAG_SCENARIO=good -- test --name ub
  sudo iptables -I FORWARD 1 -p tcp --dport 443 -m string --string "quay.io" --algo bm -j REJECT --reject-with tcp-reset
  obs "[UB2] quay.io from a container: $(docker run --rm curlimages/curl:8.11.1 -sS -m 10 -o /dev/null -w '%{http_code}' https://quay.io/v2/ 2>&1 | tail -1)"
  run_case UB2-quay-blocked-warm 900 DIAG_SCENARIO=good -- test --name ub
  local dl=$SRC/.devenv/ub/dagger.log
  obs "[UB2] dagger.log lines with panic or proxy errors: $(grep -ciE 'panic|connection reset|proxyconnect' "$dl")"
  grep -iE 'panic|connection reset|exit code' "$dl" | head -10
  sudo iptables -D FORWARD 1

  local out=$RUNNER_TEMP/out-UB3-ctrlc.log t=$SECONDS pid
  touch "$RUNNER_TEMP/stamp-UB3-ctrlc"
  (cd "$SRC" && DIAG_SCENARIO=manifests exec "$DIAG" up --name ub) >"$out" 2>&1 &
  pid=$!
  while kill -0 $pid 2>/dev/null && ! grep -q '\] management packages' "$out" && [ $((SECONDS - t)) -lt 1200 ]; do sleep 5; done
  "$PAUSE" 60
  local s=$SECONDS
  kill -INT $pid
  wait_gone $pid 300
  wait $pid
  obs "[UB3-ctrlc] up exit=$? $((SECONDS - s))s after Ctrl-C during the PackageInstalls gate"
  tail -8 "$out"
  inspect UB3-ctrlc ub
}

# setup_greeting copies the example to $G, as its own repo that builds images from its own go.mod,
# and builds its CLI against this checkout of the tool.
G=$RUNNER_TEMP/greeting
setup_greeting() {
  local t=$SECONDS
  cp -a "$REPO/examples/greeting" "$G"
  (cd "$G" && git init -q && git add -A && git -c user.email=x@example.com -c user.name=x commit -qm init)
  "$REPO/explore/lib/adopter.sh" "$REPO/examples/greeting" "$RUNNER_TEMP/greeting-build" >/dev/null 2>&1 || echo "adopter failed"
  (cd "$RUNNER_TEMP/greeting-build" && GOWORK=off go build -o "$RUNNER_TEMP/greeting-devenv" ./cmd/devenv) || echo "build failed"
  obs "greeting setup took $((SECONDS - t))s"
}

flake() {
  for i in 1 2 3 4 5 6 7; do
    [ "$(left)" -gt 540 ] || break
    run_case F$i 900 DIAG_SCENARIO=good -- test --name fl
  done
  obs "flake: $(grep -l 'passed\.' "$RUNNER_TEMP"/out-F*.log | wc -l) of $(ls "$RUNNER_TEMP"/out-F*.log | wc -l) passed"
  grep -h -B2 '^Error' "$RUNNER_TEMP"/out-F*.log
}

greetingok() {
  setup_greeting
  for i in 1 2 3; do
    [ "$(left)" -gt 720 ] || break
    RUN_DIR=$G RUN_BIN=$RUNNER_TEMP/greeting-devenv run_case GOK$i 1200 -- test --name gok
  done
  obs "greeting: $(grep -l 'passed\.' "$RUNNER_TEMP"/out-GOK*.log | wc -l) of $(ls "$RUNNER_TEMP"/out-GOK*.log | wc -l) passed"
}

# syncer makes the example's greeting-syncer, a Management package, exit on start.
syncer() {
  setup_greeting
  local yaml=$G/config/greeting-syncer/greeting-syncer.yaml t=$SECONDS pid
  sed -i 's|^        image: greeting-syncer$|        image: greeting-syncer\n        args: ["--metrics-bind-address=:8443"]|' "$yaml"
  grep -n -A1 'image: greeting-syncer' "$yaml"
  pid=$(cd "$G" && "$REPO/explore/lib/up-bg.sh" "$RUNNER_TEMP/out-S1-up.log" 1200 -- "$RUNNER_TEMP/greeting-devenv" up --name sy)
  obs "[S1-up] up printed 'is up': ${pid:+yes} after $((SECONDS - t))s"
  grep -v '^\s*$' "$RUNNER_TEMP/out-S1-up.log" | tail -8
  if [ -n "$pid" ]; then
    local k=$G/.devenv/sy/mgmt.kubeconfig
    obs "[S1-up] PackageInstalls: $(kubectl --kubeconfig "$k" get pkgi -A --no-headers 2>&1 | tr -s ' ' | tr '\n' ';')"
    obs "[S1-up] greeting-syncer pods: $(kubectl --kubeconfig "$k" get pods -n greeting-syncer --no-headers 2>&1 | tr -s ' ' | tr '\n' ';')"
    (cd "$G" && timeout 300 "$RUNNER_TEMP/greeting-devenv" down --name sy) 2>&1 | tail -2
    wait_gone "$pid" 120
  fi
  RUN_DIR=$G RUN_BIN=$RUNNER_TEMP/greeting-devenv run_case S2-test 1500 -- test --name sy
  RUN_DIR=$G inspect S2-test sy
  RUN_DIR=$G grep_logs S2-test sy 'flag provided but not defined' "greeting-syncer's own error"
}

# probe gives the example's addon-manager a readiness probe that never passes.
probe() {
  setup_greeting
  local yaml=$G/config/addon-manager/addon-manager.yaml
  sed -i 's|^        image: addon-manager$|        image: addon-manager\n        readinessProbe:\n          httpGet:\n            path: /readyz\n            port: 8081|' "$yaml"
  tail -6 "$yaml"
  RUN_DIR=$G RUN_BIN=$RUNNER_TEMP/greeting-devenv run_case R1-probe 1500 -- test --name pr
  RUN_DIR=$G inspect R1-probe pr
  RUN_DIR=$G grep_logs R1-probe pr 'Readiness probe failed' "readiness probe failure"
  RUN_DIR=$G grep_logs R1-probe pr '8081' "probe port 8081"
}

greeting() {
  setup_greeting
  local yaml=$G/config/greeting-controller/greeting-controller.yaml
  cp "$yaml" "$RUNNER_TEMP/gc.yaml"
  sed -i 's|^        image: greeting-controller$|        image: greeting-controller\n        args: ["--metrics-bind-address=:8443"]|' "$yaml"
  grep -n -A1 'image: greeting-controller' "$yaml"
  RUN_DIR=$G RUN_BIN=$RUNNER_TEMP/greeting-devenv run_case GC1-workload-crash 1500 -- test --name gc
  RUN_DIR=$G inspect GC1-workload-crash gc
  RUN_DIR=$G grep_logs GC1-workload-crash gc 'flag provided but not defined' "greeting-controller's own error"
  cp "$RUNNER_TEMP/gc.yaml" "$yaml"
  if [ "$(left)" -gt 840 ]; then
    sed -i 's|^  helloImage: hello$|  helloImage: helo|' "$yaml"
    grep -n 'helloImage' "$yaml"
    RUN_DIR=$G RUN_BIN=$RUNNER_TEMP/greeting-devenv run_case GC2-workload-typo 1200 -- test --name gc
    RUN_DIR=$G inspect GC2-workload-typo gc
  fi
}

echo "OBS: runner $(uname -m) $(nproc) cpus, $(free -g | awk '/Mem/{print $2}') GB"
setup
"$1"
echo "===== SUMMARY ($1)"
cat "$SUMMARY"
