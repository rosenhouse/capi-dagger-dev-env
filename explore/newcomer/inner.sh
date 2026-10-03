#!/usr/bin/env bash
# Which edits change deployed images: the README's kubeconfig file in the module, a docs edit. What fills dagger.log.
here=$(cd "$(dirname "$0")" && pwd)
source "$here/lib.sh"
ensure_kubectl
cd "$here/../../examples/greeting"
go build -o /tmp/devenv ./cmd/devenv
D() { /tmp/devenv "$@"; }
start_up "$RUNNER_TEMP/i.log" /tmp/devenv up --name i
wait_up "$RUNNER_TEMP/i.log" 1200 || { dump_state .devenv/i; summary; exit 1; }
M="kubectl --kubeconfig .devenv/i/mgmt.kubeconfig"
W="kubectl --kubeconfig .devenv/i/workload.kubeconfig"
images() {
  echo "addon-manager=$($M -n addon-manager get deploy addon-manager -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*@sha256:\(.......\).*/\1/')" \
    "greeting-syncer=$($M -n greeting-syncer get deploy greeting-syncer -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*@sha256:\(.......\).*/\1/')" \
    "greeting-controller=$($W -n greeting-controller get deploy greeting-controller -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*@sha256:\(.......\).*/\1/')"
}
echo "################ dagger.log after up"
log=.devenv/i/dagger.log
note "dagger.log: $(wc -l <"$log") lines; 'level=debug' lines: $(grep -c 'level=debug' "$log"); containerd.services lines: $(grep -c 'containerd.services' "$log"); lines mentioning kind/kubectl/clusterctl: $(grep -c -E 'kind create|kubectl |clusterctl ' "$log")"
echo "--- most common line shapes"; sed -E 's/^[0-9]+ *: //; s/[0-9a-f]{12,}//g; s/[0-9]+(\.[0-9]+)?s//g' "$log" | cut -c1-60 | sort | uniq -c | sort -rn | head -15

echo "################ no-op redeploy"
a=$(images); timed "no-op redeploy" D redeploy; b=$(images)
note "images after no-op redeploy: $([ "$a" = "$b" ] && echo unchanged || echo "changed: $a -> $b")"

echo "################ README step: kubeconfig into the module directory, then redeploy"
D kubeconfig --cluster workload > workload.kubeconfig
git status --porcelain
a=$(images); timed "redeploy after writing workload.kubeconfig" D redeploy; b=$(images)
note "images after writing workload.kubeconfig: $([ "$a" = "$b" ] && echo unchanged || echo "changed: $a -> $b")"
rm workload.kubeconfig

echo "################ docs-only edit"
echo "A docs edit." >> README.md
a=$(images); timed "redeploy after editing README.md" D redeploy; b=$(images)
note "images after README.md edit: $([ "$a" = "$b" ] && echo unchanged || echo "changed: $a -> $b")"
git checkout README.md

echo "################ edit only a _test.go file"
echo "// edit" >> internal/greetingsyncer/reconciler_test.go
a=$(images); timed "redeploy after editing a test file" D redeploy; b=$(images)
note "images after test-only edit: $([ "$a" = "$b" ] && echo unchanged || echo "changed: $a -> $b")"
git checkout internal/greetingsyncer/reconciler_test.go

note "dagger.log at end: $(wc -l <"$log") lines, $(du -h "$log" | cut -f1)"
D down --name i --purge
summary
