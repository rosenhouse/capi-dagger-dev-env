#!/usr/bin/env bash
# Redeploying a state seen earlier in the session, pruning, and a Flux consumer.
source "$(dirname "$0")/common.sh"
setup_greeting
up_bg
cd "$G"
greeting "hello one"
obs "hello serves after $(wait_hello 'hello one' 300)s"
LOG=$G/.devenv/$NAME/dagger.log
syncer_bundle() { mk -n kapp-controller-packaging-global get packages.data.packaging.carvel.dev greeting-syncer.demo.example.com.0.1.0 -o jsonpath='{.spec.template.spec.fetch[0].imgpkgBundle.image}' | sed 's/.*@sha256://' | cut -c1-12; }

section "R1: --version a, then b, then a again"
rd va --version a; obs "after a: Package bundle $(syncer_bundle); hello: $(wait_hello '(hello a)' 120)s $(hello_body)"
rd vb --version b; obs "after b: Package bundle $(syncer_bundle); hello: $(wait_hello '(hello b)' 120)s $(hello_body)"
rd va2 --version a; obs "after a again: Package bundle $(syncer_bundle); hello: $(hello_body)"
section "R1 dagger.log: the Package reapply execs"
grep -n 'force-conflicts' "$LOG" | grep -i -E 'cached|done|withExec' | tail -12 | cut -c1-200

section "R2: a fresh state recovers"
rd vc --version c; obs "after c: Package bundle $(syncer_bundle); hello: $(wait_hello '(hello c)' 120)s $(hello_body)"

section "R3: prune a removed ConfigMap (with another change, so the state is new)"
cat >config/greeting-syncer/explore-extra.yaml <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: explore-extra
  namespace: greeting-syncer
data:
  k: v
EOF
rd prune-add --version p1; obs "ConfigMap after add: $(mk -n greeting-syncer get cm explore-extra -o name 2>&1)"
rm config/greeting-syncer/explore-extra.yaml
rd prune-remove --version p2; obs "ConfigMap after remove: $(mk -n greeting-syncer get cm explore-extra -o name 2>&1)"

section "R4: a manual tweak to a Deployment across a redeploy"
mk -n greeting-syncer set env deploy/greeting-syncer EXPLORE_DEBUG=1 >/dev/null
mk -n greeting-syncer rollout status deploy/greeting-syncer --timeout=120s >/dev/null
rd tweak --version t1
obs "EXPLORE_DEBUG after redeploy: '$(mk -n greeting-syncer get deploy greeting-syncer -o jsonpath='{.spec.template.spec.containers[0].env}')'"

section "F1: install Flux on the management cluster"
t=$SECONDS
curl -sSL -o "$W/flux-install.yaml" https://github.com/fluxcd/flux2/releases/latest/download/install.yaml
mk apply -f "$W/flux-install.yaml" >/dev/null
mk -n flux-system wait --for=condition=Available deploy --all --timeout=300s
obs "flux install rc=$? in $((SECONDS - t))s: $(grep -m1 'app.kubernetes.io/version' "$W/flux-install.yaml")"

section "F2: what a Workload package publishes"
ref=$(mk -n kapp-controller-packaging-global get packages.data.packaging.carvel.dev greeting-controller.demo.example.com.0.1.0 -o jsonpath='{.spec.template.spec.fetch[0].imgpkgBundle.image}')
obs "greeting-controller bundle ref: $ref"
mk run bundle-peek --restart=Never --image=gcr.io/go-containerregistry/crane:debug --command -- sh -c \
  "crane manifest --insecure $ref; echo; crane export --insecure $ref - | tar -t; crane export --insecure $ref - | tar -xO config/greeting-controller.yaml | grep -n -i image"
mk wait --for=jsonpath='{.status.phase}'=Succeeded pod/bundle-peek --timeout=180s
mk logs bundle-peek 2>&1 | head -40

section "F3: Flux OCIRepository + Kustomization into the workload cluster"
mk apply -f - <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: greeting-controller, namespace: default}
spec:
  interval: 1m
  url: oci://${ref%@*}
  ref: {digest: ${ref#*@}}
  insecure: true
  layerSelector: {mediaType: application/vnd.oci.image.layer.v1.tar+gzip, operation: extract}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: work-greeting-controller-flux, namespace: default}
spec:
  interval: 1m
  sourceRef: {kind: OCIRepository, name: greeting-controller}
  path: ./config
  prune: true
  wait: true
  timeout: 2m
  kubeConfig: {secretRef: {name: work-kubeconfig}}
EOF
sleep 90
mk -n default get ocirepository,kustomization 2>&1
obs "OCIRepository: $(mk -n default get ocirepository greeting-controller -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>&1)"
obs "Kustomization: $(mk -n default get kustomization work-greeting-controller-flux -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>&1 | cut -c1-700)"

section "end"
"$DEVENV" down --name "$NAME"
summary
