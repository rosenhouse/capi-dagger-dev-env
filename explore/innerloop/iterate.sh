#!/usr/bin/env bash
# Edit -> redeploy cycles on a running greeting environment.
source "$(dirname "$0")/common.sh"
setup_greeting
cd "$G"

section "A0: commands while up is coming up"
"$DEVENV" up --name "$NAME" >"$W/up.log" 2>&1 &
UP_PID=$!
t0=$SECONDS
until [ -e "$G/.devenv/$NAME/lock" ] || [ $((SECONDS - t0)) -gt 60 ]; do sleep 1; done
sleep 15
for cmd in "status" "kubeconfig --name $NAME --cluster workload" "redeploy --name $NAME"; do
  t1=$SECONDS
  out=$("$DEVENV" $cmd 2>&1); rc=$?
  obs "while coming up: '$cmd' rc=$rc in $((SECONDS - t1))s: $(echo "$out" | tr '\n' ' ' | cut -c1-300)"
done
end=$((t0 + 1500))
until grep -q " is up\." "$W/up.log" || [ $SECONDS -gt $end ] || ! kill -0 $UP_PID 2>/dev/null; do sleep 5; done
if ! grep -q " is up\." "$W/up.log"; then obs "up FAILED"; tail -60 "$W/up.log"; dump_state; summary; exit 1; fi
obs "up took $((SECONDS - t0))s"
cat "$W/up.log"

section "A1: baseline"
greeting "hello one"
obs "hello serves greeting after $(wait_hello 'hello one' 300)s; body: $(hello_body)"
snap "$W/s0"; cat "$W/s0"
mk get pkgi,apps -A 2>&1
mk get packages -A 2>&1
mk -n greeting-syncer get deploy greeting-syncer -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'

section "A2: no-op redeploys"
rd noop1; snap "$W/s1"; obs "noop1 rolled: $(rolled "$W/s0" "$W/s1")"
rd noop2; snap "$W/s2"; obs "noop2 rolled: $(rolled "$W/s1" "$W/s2")"

section "A3: YAML-only change (new ConfigMap in greeting-syncer config)"
cat >config/greeting-syncer/explore-extra.yaml <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: explore-extra
  namespace: greeting-syncer
data:
  k: v
EOF
rd yaml-add; snap "$W/s3"; obs "yaml-add rolled: $(rolled "$W/s2" "$W/s3")"
obs "explore-extra ConfigMap: $(mk -n greeting-syncer get cm explore-extra -o name 2>&1)"

section "A4: remove the ConfigMap from config"
rm config/greeting-syncer/explore-extra.yaml
rd yaml-remove; snap "$W/s4"; obs "yaml-remove rolled: $(rolled "$W/s3" "$W/s4")"
obs "explore-extra ConfigMap after removal: $(mk -n greeting-syncer get cm explore-extra -o name 2>&1)"

section "A5: hello-only code change"
sed -i 's|"%s (hello %s)\\n"|"%s (hello %s) edited\\n"|' cmd/hello/main.go
grep -n 'edited' cmd/hello/main.go
t5=$SECONDS
rd hello-edit; snap "$W/s5"; obs "hello-edit rolled: $(rolled "$W/s4" "$W/s5")"
obs "after redeploy returned, hello body: $(hello_body)"
obs "hello serves edited code $(wait_hello 'edited' 300)s after redeploy returned ($((SECONDS - t5))s after start)"

section "A6: greeting-syncer-only code change"
sed -i 's|log := ctrl.Log.WithName("greeting-syncer")|log := ctrl.Log.WithName("greeting-syncer")\n\tlog.Info("explore build marker A6")|' cmd/greeting-syncer/main.go
rd syncer-edit; snap "$W/s6"; obs "syncer-edit rolled: $(rolled "$W/s5" "$W/s6")"
obs "syncer log marker: $(mk -n greeting-syncer logs deploy/greeting-syncer 2>&1 | grep -c 'explore build marker A6')"

section "A7: --version"
rd version1 --version explore1
obs "hello body after --version explore1: $(wait_hello 'explore1' 300)s: $(hello_body)"
rd version1-again --version explore1; snap "$W/s7a"
rd version-default; snap "$W/s7"; obs "back to default version rolled: $(rolled "$W/s7a" "$W/s7")"
obs "hello body with default version: $(wait_hello 'edited' 120)s: $(hello_body)"
mk get pkgi,packages -A 2>&1
mk -n devenv get app greeting-syncer -o jsonpath='{.status.fetch.stdout}' 2>&1 | head -5; echo
mk -n devenv get app greeting-syncer -o jsonpath='{.spec.fetch}{"\n"}' 2>&1
"$DEVENV" status 2>&1
rd bad-version --version 'v1/x'

section "A8: compile error"
cp cmd/greeting-syncer/main.go "$W/main.go.bak"
echo 'var broken int = "not an int"' >>cmd/greeting-syncer/main.go
rd compile-error
obs "compile-error mentions file: $(grep -c 'main.go' "$W/rd-compile-error.log") lines; mentions 'cannot use': $(grep -c 'cannot use' "$W/rd-compile-error.log")"
cat "$W/rd-compile-error.log" | head -60
cp "$W/main.go.bak" cmd/greeting-syncer/main.go
obs "env usable after compile error: $(mk get nodes --no-headers 2>&1 | wc -l) mgmt nodes; status: $("$DEVENV" status 2>&1 | tail -1)"
tail -5 "$W/up.log"

section "A9: concurrent redeploys"
sed -i 's|edited|edited-A9|' cmd/hello/main.go
rd conc1 & p1=$!
sleep 2
rd conc2 & p2=$!
wait $p1; wait $p2

section "A10: Ctrl-C a redeploy midway"
sed -i 's|edited-A9|edited-A10|' cmd/hello/main.go
"$DEVENV" redeploy --name "$NAME" >"$W/rd-ctrlc.log" 2>&1 &
rp=$!
sleep 10
t10=$SECONDS
kill -INT $rp
for i in $(seq 1 20); do kill -0 $rp 2>/dev/null || break; sleep 1; done
kill -0 $rp 2>/dev/null && { obs "redeploy ignored SIGINT for 20s; sending TERM"; kill -TERM $rp; }
wait $rp; obs "interrupted redeploy exit=$? after $((SECONDS - t10))s; output: $(tr '\n' ' ' <"$W/rd-ctrlc.log")"
obs "up still alive: $(kill -0 $UP_PID 2>/dev/null && echo yes || echo no)"
rd after-ctrlc-immediate
sleep 20
rd after-ctrlc
obs "hello after ctrl-c sequence: $(wait_hello 'edited-A10' 300)s: $(hello_body)"
tail -15 "$W/up.log"

section "A11: rename a Deployment"
perl -0pi -e 's/(kind: Deployment\nmetadata:\n  name: )greeting-syncer/${1}greeting-syncer-renamed/' config/greeting-syncer/greeting-syncer.yaml
grep -n 'renamed' config/greeting-syncer/greeting-syncer.yaml
rd rename
obs "deployments in greeting-syncer ns after rename: $(mk -n greeting-syncer get deploy --no-headers 2>&1 | awk '{print $1, $2}' | tr '\n' ';')"

section "A12: CRD: add a field"
perl -0pi -e 's/(              message:\n                minLength: 1\n                type: string\n)/$1              exploreField:\n                type: string\n/' config/greeting-syncer/demo.example.com_greetings.yaml
grep -n exploreField config/greeting-syncer/demo.example.com_greetings.yaml
rd crd-field
obs "CRD has exploreField: $(mk get crd greetings.demo.example.com -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.exploreField}' 2>&1)"

section "A13: change devenv.Config (new package) while up runs"
mkdir -p config/explore-extra
cat >config/explore-extra/extra.yaml <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: explore-extra
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: extra
  namespace: explore-extra
spec:
  selector:
    matchLabels: {app: extra}
  template:
    metadata:
      labels: {app: extra}
    spec:
      containers:
      - name: hello
        image: hello
EOF
perl -0pi -e 's/(\t\t\{Name: "greeting-controller")/\t\t{Name: "explore-extra", RefName: "explore-extra.demo.example.com", Config: "config\/explore-extra", Images: []string{"hello"}},\n$1/' cmd/devenv/main.go
grep -n explore-extra cmd/devenv/main.go
go build -o "$W/devenv2" ./cmd/devenv && obs "rebuilt CLI with a new package"
RD_BIN="$W/devenv2" rd config-change
obs "explore-extra namespace after redeploy with new Config: $(mk get ns explore-extra -o name 2>&1)"
obs "packages: $(mk get packages -A --no-headers 2>&1 | awk '{print $2}' | tr '\n' ' ')"
obs "pkgi: $(mk get pkgi -A --no-headers 2>&1 | awk '{print $2}' | tr '\n' ' ')"

section "A14: down"
t14=$SECONDS
"$DEVENV" down --name "$NAME" 2>&1; obs "down rc=$? took $((SECONDS - t14))s"
"$DEVENV" status
tail -5 "$W/up.log"
summary
