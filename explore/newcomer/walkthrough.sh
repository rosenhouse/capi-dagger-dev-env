#!/usr/bin/env bash
# A newcomer follows the README literally: go run, no --name, kubectl against both clusters, edit, redeploy, down.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
ensure_kubectl
cd "$here/../../examples/greeting"
obs "runner $(nproc) cpus, $(free -g | awk '/Mem/{print $2}') GB, cgroup $(stat -fc %T /sys/fs/cgroup), disk free $(df -BG --output=avail / | tail -1)"
df_before=$(df -BG --output=used / | tail -1)
D() { go run ./cmd/devenv "$@"; }

echo "################ up (README: go run ./cmd/devenv up)"
start_up "$RUNNER_TEMP/up.log" go run ./cmd/devenv up
wait_up "$RUNNER_TEMP/up.log" 1500 || { dump_state .devenv/*; summary; exit 1; }
echo "--- full up output"; cat "$RUNNER_TEMP/up.log"
name=$(sed -n 's/^Environment \(.*\) is up\.$/\1/p' "$RUNNER_TEMP/up.log")
note "environment name: $name"
printed=$(sed -n 's/^After changing code, run: //p' "$RUNNER_TEMP/up.log")
note "up prints redeploy command: '$printed'"
echo "--- running the printed command verbatim"
bash -c "$printed" >"$RUNNER_TEMP/printed.out" 2>&1; note "printed redeploy command exit $?: $(tail -1 "$RUNNER_TEMP/printed.out")"
note "dagger.log after up: $(wc -l < .devenv/$name/dagger.log) lines, $(du -h .devenv/$name/dagger.log | cut -f1)"
echo "--- dagger.log head"; head -20 .devenv/$name/dagger.log
echo "--- dagger.log tail"; tail -20 .devenv/$name/dagger.log
find .devenv -maxdepth 3 | sort

echo "################ status"
timed "go run status (first)" D status
timed "go run status (second)" D status

echo "################ kubeconfig (README)"
timed "kubeconfig mgmt" D kubeconfig > "$RUNNER_TEMP/mgmt.kubeconfig"
timed "kubeconfig workload" D kubeconfig --cluster workload > "$RUNNER_TEMP/workload.kubeconfig"
for c in mgmt workload; do
  echo "--- $c kubeconfig contexts"
  kubectl --kubeconfig "$RUNNER_TEMP/$c.kubeconfig" config get-contexts
  grep server: "$RUNNER_TEMP/$c.kubeconfig"
done
diff <(cat .devenv/$name/mgmt.kubeconfig) "$RUNNER_TEMP/mgmt.kubeconfig" >/dev/null && obs "kubeconfig output equals .devenv file"

echo "################ merge into ~/.kube/config and switch contexts"
mkdir -p ~/.kube
cat > ~/.kube/config <<'EOF'
apiVersion: v1
kind: Config
clusters:
- cluster: {server: https://example.invalid:6443}
  name: work-cluster
contexts:
- context: {cluster: work-cluster, user: work-user}
  name: work-context
current-context: work-context
users:
- name: work-user
  user: {token: x}
EOF
KUBECONFIG=~/.kube/config:$RUNNER_TEMP/mgmt.kubeconfig:$RUNNER_TEMP/workload.kubeconfig kubectl config view --flatten > "$RUNNER_TEMP/merged" && cp "$RUNNER_TEMP/merged" ~/.kube/config
kubectl config get-contexts
kubectl config use-context "$name-mgmt" && kubectl get nodes -o wide; note "merged mgmt context get nodes exit $?"
kubectl --context "$name-workload" get nodes -o wide; note "merged workload context get nodes exit $?"
export KUBECONFIG=$RUNNER_TEMP/mgmt.kubeconfig

echo "################ where do things run (mgmt)"
kubectl get ns
kubectl get deploy -A
kubectl get pods -A | grep -v Running | head -20
kubectl get clusters,machines -A
kubectl get packageinstalls,apps -A
kubectl get packages -A
kubectl get packagerepositories -A
echo "--- controller logs (mgmt)"
for d in addon-manager/addon-manager greeting-syncer/greeting-syncer; do
  kubectl -n "${d%/*}" logs "deploy/${d#*/}" --tail=5; note "logs deploy/${d#*/} -n ${d%/*} exit $?"
done
kubectl -n devenv get app -o wide
kubectl -n default get app -o wide

echo "################ where do things run (workload)"
export KUBECONFIG=$RUNNER_TEMP/workload.kubeconfig
kubectl get nodes -o wide
kubectl get ns
kubectl get deploy -A
kubectl -n greeting-controller logs deploy/greeting-controller --tail=5; note "workload greeting-controller logs exit $?"

echo "################ try the example by hand"
export KUBECONFIG=$RUNNER_TEMP/mgmt.kubeconfig
cat <<'EOF' | kubectl apply -f -
apiVersion: demo.example.com/v1alpha1
kind: Greeting
metadata: {name: hi, namespace: default}
spec: {clusterName: work, message: "hi there"}
EOF
s=$SECONDS
for i in $(seq 60); do
  body=$(kubectl --kubeconfig "$RUNNER_TEMP/workload.kubeconfig" get --raw /api/v1/namespaces/default/services/hi-proxy:80/proxy/ 2>&1) && break
  sleep 5
done
note "hand-made Greeting served after $((SECONDS - s))s: '$body'"

echo "################ edit hello and redeploy (README: go run ./cmd/devenv redeploy)"
sed -i 's|"%s (hello %s)\\n"|"%s [edited hello %s]\\n"|' cmd/hello/main.go
git diff --stat
timed "redeploy after editing hello" D redeploy
s=$SECONDS
for i in $(seq 60); do
  body=$(kubectl --kubeconfig "$RUNNER_TEMP/workload.kubeconfig" get --raw /api/v1/namespaces/default/services/hi-proxy:80/proxy/ 2>&1)
  case $body in *edited*) break;; esac
  sleep 5
done
note "after redeploy returned, edited hello visible after $((SECONDS - s))s: '$body'"
kubectl --kubeconfig "$RUNNER_TEMP/workload.kubeconfig" -n default get pods -o wide

timed "no-op redeploy" D redeploy

echo "################ edit a controller that runs on mgmt and redeploy"
grep -n 'func main' cmd/greeting-syncer/main.go | head -2
sed -i '0,/^func main() {/s//func main() {\n\tprintln("edited greeting-syncer")/' cmd/greeting-syncer/main.go
timed "redeploy after editing greeting-syncer" D redeploy
note "'edited greeting-syncer' lines in greeting-syncer logs: $(kubectl --kubeconfig "$RUNNER_TEMP/mgmt.kubeconfig" -n greeting-syncer logs deploy/greeting-syncer 2>&1 | grep -c 'edited greeting-syncer')"
kubectl --kubeconfig "$RUNNER_TEMP/mgmt.kubeconfig" -n greeting-syncer get pods

echo "################ redeploy with a compile error"
echo 'func broken() { undefinedThing() }' >> cmd/hello/main.go
timed_tail "redeploy with compile error" 30 D redeploy
D status
kubectl --kubeconfig "$RUNNER_TEMP/workload.kubeconfig" get nodes >/dev/null; note "workload API after failed redeploy: exit $?"
git checkout cmd/hello/main.go
echo "--- up's own output since it came up"; sed -n '/is up\./,$p' "$RUNNER_TEMP/up.log"

echo "################ redeploy with a broken ytt config"
cp config/greeting-syncer/greeting-syncer.yaml "$RUNNER_TEMP/gs.yaml"
printf -- '---\n#@ load("nonexistent.star", "x")\n' >> config/greeting-syncer/greeting-syncer.yaml
timed_tail "redeploy with broken ytt" 30 D redeploy
cp "$RUNNER_TEMP/gs.yaml" config/greeting-syncer/greeting-syncer.yaml
kubectl --kubeconfig "$RUNNER_TEMP/mgmt.kubeconfig" get apps -A
timed_tail "redeploy after fixing ytt" 10 D redeploy

echo "################ down (README)"
timed "go run down" D down
kill -0 "$UP_PID" 2>/dev/null && note "up still running after down returned" || { wait "$UP_PID"; note "up exited $? after down"; }
echo "--- up's output after down"; sed -n '/Press Ctrl-C/,$p' "$RUNNER_TEMP/up.log"
D status
ls -la .devenv/$name
kubectl --kubeconfig ".devenv/$name/mgmt.kubeconfig" get nodes --request-timeout=10s >"$RUNNER_TEMP/k.out" 2>&1; note "kubectl with leftover kubeconfig after down: exit $?: $(tail -1 "$RUNNER_TEMP/k.out")"
D kubeconfig 2>&1 | tail -2

echo "################ down --purge (README)"
timed "go run down --purge" D down --purge
D status
ls -la .devenv 2>&1
ls -la ~/.cache/devenv 2>&1
df_after=$(df -BG --output=used / | tail -1)
note "disk used before up: $df_before, after purge: $df_after"
docker ps -a --format '{{.Names}} {{.Image}} {{.Status}}'
docker system df
summary
