#!/usr/bin/env bash
# Broken redeploys on a running greeting environment, and recovery from them.
source "$(dirname "$0")/common.sh"
setup_greeting
up_bg
cd "$G"
greeting "hello one"
obs "hello serves after $(wait_hello 'hello one' 300)s"

# watch_apps LABEL prints the Apps' state every 30s in the background.
watch_apps() {
  while true; do
    echo "[apps $1 t=${SECONDS}s] $(mk get apps -A --no-headers 2>&1 | awk '{print $2": "$3" "$4" "$5}' | tr '\n' ';')"
    sleep 30
  done
}

# budget SECONDS NAME fails, and says so, once the job has run longer than SECONDS.
budget() { [ $SECONDS -lt "$1" ] || { obs "skipped $2: out of time at ${SECONDS}s"; return 1; }; }

section "B1: management controller crashloops"
cp cmd/greeting-syncer/main.go "$W/syncer.bak"
sed -i 's|log := ctrl.Log.WithName("greeting-syncer")|log := ctrl.Log.WithName("greeting-syncer")\n\tlog.Error(nil, "explore: crashing on purpose"); os.Exit(3)|' cmd/greeting-syncer/main.go
watch_apps crash & wp=$!
rd crashloop
kill $wp
mk -n greeting-syncer get pods 2>&1
mk -n devenv get app greeting-syncer -o jsonpath='{.status.deploy.stdout}' 2>&1 | tail -15; echo
obs "app status after crashloop redeploy: $(mk -n devenv get app greeting-syncer -o jsonpath='{.status.friendlyDescription}' 2>&1)"
obs "env usable after crashloop: hello body: $(hello_body)"

section "B2: fix the crash and redeploy right away"
cp "$W/syncer.bak" cmd/greeting-syncer/main.go
watch_apps fix & wp=$!
rd crash-fix || rd crash-fix-retry
kill $wp
mk -n greeting-syncer get pods 2>&1

section "B3: invalid YAML in config"
cat >config/greeting-syncer/bad.yaml <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: bad
  namespace: greeting-syncer
data: [unclosed
EOF
watch_apps badyaml & wp=$!
rd bad-yaml
kill $wp
obs "app description: $(mk -n devenv get app greeting-syncer -o jsonpath='{.status.friendlyDescription}' 2>&1)"
obs "app error: $(mk -n devenv get app greeting-syncer -o jsonpath='{.status.usefulErrorMessage}' 2>&1 | head -5 | tr '\n' ' ')"
rm config/greeting-syncer/bad.yaml
rd bad-yaml-fix

budget 1500 B5 && {
section "B5: workload-side controller crashloops"
cp cmd/greeting-controller/main.go "$W/gc.bak"
sed -i 's|log := ctrl.Log.WithName("greeting-controller")|log := ctrl.Log.WithName("greeting-controller")\n\tlog.Error(nil, "explore: crashing on purpose"); os.Exit(3)|' cmd/greeting-controller/main.go
watch_apps wcrash & wp=$!
rd workload-crashloop
kill $wp
wk -n greeting-controller get pods 2>&1
obs "workload app description: $(mk -n default get app work-greeting-controller -o jsonpath='{.status.friendlyDescription}' 2>&1)"
cp "$W/gc.bak" cmd/greeting-controller/main.go
watch_apps wfix & wp=$!
rd workload-crash-fix || rd workload-crash-fix-retry
kill $wp
wk -n greeting-controller get pods 2>&1

}

budget 2100 B4 && {
section "B4: manifest that kapp rejects (unknown kind)"
cat >config/greeting-syncer/unknown.yaml <<'EOF'
apiVersion: explore.example.com/v1
kind: Widget
metadata:
  name: w
  namespace: greeting-syncer
EOF
rd unknown-kind
obs "app error: $(mk -n devenv get app greeting-syncer -o jsonpath='{.status.usefulErrorMessage}' 2>&1 | head -5 | tr '\n' ' ')"
rm config/greeting-syncer/unknown.yaml
rd unknown-kind-fix

}

section "end"
tail -30 "$W/up.log"
"$DEVENV" down --name "$NAME"
summary
