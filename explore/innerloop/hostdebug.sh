#!/usr/bin/env bash
# Out-of-cluster controllers, debugging, and repeated edit cycles on a running greeting environment.
source "$(dirname "$0")/common.sh"
setup_greeting
up_bg
cd "$G"
MK=$G/.devenv/$NAME/mgmt.kubeconfig
greeting "hello one"
obs "hello serves after $(wait_hello 'hello one' 300)s"

section "C1: the CAPI kubeconfig Secret, from the host"
mk -n default get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d >"$W/secret.kubeconfig"
obs "work-kubeconfig Secret server: $(grep server: "$W/secret.kubeconfig")"
obs "workload.kubeconfig server: $(grep server: "$G/.devenv/$NAME/workload.kubeconfig")"
t=$SECONDS; out=$(kubectl --kubeconfig "$W/secret.kubeconfig" --request-timeout=15s get nodes 2>&1 | tail -2 | tr '\n' ' ')
obs "kubectl with the Secret's kubeconfig from the host ($((SECONDS - t))s): $out"

section "C2: run greeting-syncer on the host, in-cluster copy scaled to 0"
go build -o "$W/greeting-syncer" ./cmd/greeting-syncer
mk -n greeting-syncer scale deploy/greeting-syncer --replicas=0
KUBECONFIG=$MK "$W/greeting-syncer" >"$W/host-syncer.log" 2>&1 &
hp=$!
sleep 5
greeting "from host syncer"
obs "host-run syncer: workload serves new message: $(wait_hello 'from host syncer' 120)"
kill $hp
obs "host-run syncer log (last lines):"
tail -15 "$W/host-syncer.log" | cut -c1-400
obs "host-run syncer errors: $(grep -c -i error "$W/host-syncer.log")"

section "C3: in-cluster copy stays at 0 across redeploys"
rd scaled0-noop
obs "replicas after no-op redeploy: $(mk -n greeting-syncer get deploy greeting-syncer -o jsonpath='{.spec.replicas}')"
sed -i 's|log := ctrl.Log.WithName("greeting-syncer")|log := ctrl.Log.WithName("greeting-syncer")\n\tlog.Info("explore C3")|' cmd/greeting-syncer/main.go
rd scaled0-change
obs "replicas after redeploy of a syncer change: $(mk -n greeting-syncer get deploy greeting-syncer -o jsonpath='{.spec.replicas}')"
mk -n greeting-syncer scale deploy/greeting-syncer --replicas=1
mk -n greeting-syncer rollout status deploy/greeting-syncer --timeout=120s
obs "message reaches workload after scaling back: $(wait_hello 'from host syncer' 180)"

section "C4: logs, port-forward, exec, debug"
obs "mgmt syncer logs: $(mk -n greeting-syncer logs deploy/greeting-syncer --tail=2 2>&1 | cut -c1-200 | tr '\n' ' ')"
obs "workload controller logs: $(wk -n greeting-controller logs deploy/greeting-controller --tail=2 2>&1 | cut -c1-200 | tr '\n' ' ')"
mk -n greeting-syncer port-forward deploy/greeting-syncer 18080:8080 >"$W/pf1.log" 2>&1 & pf1=$!
wk -n greeting-controller port-forward deploy/greeting-controller 18081:8080 >"$W/pf2.log" 2>&1 & pf2=$!
sleep 6
obs "mgmt metrics via port-forward: $(curl -s -m 10 localhost:18080/metrics | grep -c '^controller_runtime') controller_runtime lines; pf log: $(tr '\n' ' ' <"$W/pf1.log")"
obs "workload metrics via port-forward: $(curl -s -m 10 localhost:18081/metrics | grep -c '^controller_runtime') controller_runtime lines; pf log: $(tr '\n' ' ' <"$W/pf2.log")"
kill $pf1 $pf2
obs "kubectl exec sh into distroless: $(mk -n greeting-syncer exec deploy/greeting-syncer -- sh -c true 2>&1 | tail -1)"
pod=$(mk -n greeting-syncer get pod -o name | head -1)
mk -n greeting-syncer debug "$pod" --image=busybox:1.37 --target=manager --container=dbg -- sh -c 'ls -la /proc/1/root/; cat /proc/1/cmdline; echo' >"$W/debug.log" 2>&1
sleep 15
obs "kubectl debug: $(tr '\n' ' ' <"$W/debug.log" | cut -c1-300)"
obs "debug container output: $(mk -n greeting-syncer logs "$pod" -c dbg 2>&1 | tr '\n' ' ' | cut -c1-400)"
obs "image refs users see: $(mk -n greeting-syncer get deploy greeting-syncer -o jsonpath='{.spec.template.spec.containers[0].image}')"

section "C5: repeated edit cycles of hello"
df -h / | tail -1
for i in 1 2 3 4 5; do
  CYC=cycle$i perl -pi -e 's/\(hello %s\)[^"]*"/(hello %s) $ENV{CYC}\\n"/' cmd/hello/main.go
  grep -n 'cycle' cmd/hello/main.go
  t=$SECONDS
  rd "cycle$i"
  obs "cycle$i: hello serves new code $(wait_hello "cycle$i" 300)s after redeploy returned; total $((SECONDS - t))s"
done
df -h / | tail -1
docker exec "$(docker ps --filter name=dagger-engine -q | head -1)" sh -c 'du -sh /var/lib/dagger 2>/dev/null' 2>&1 | tail -1

section "C6: CRD: remove the stored version"
sed -i 's/    name: v1alpha1/    name: v1alpha2/' config/greeting-syncer/demo.example.com_greetings.yaml
grep -n 'name: v1alpha' config/greeting-syncer/demo.example.com_greetings.yaml
rd crd-version-removed
obs "app error: $(mk -n devenv get app greeting-syncer -o jsonpath='{.status.usefulErrorMessage}' 2>&1 | head -8 | tr '\n' ' ')"

section "end"
tail -30 "$W/up.log"
"$DEVENV" down --name "$NAME"
summary
