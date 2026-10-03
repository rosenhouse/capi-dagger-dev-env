#!/usr/bin/env bash
# Running the consumer's Test against a running environment, a Flux consumer of a Workload bundle, and disk use.
source "$(dirname "$0")/common.sh"
setup_greeting
obs "disk before up: $(df --output=used -BG / | tail -1)"
up_bg
cd "$G"

section "E1: devenv test against the running environment"
t=$SECONDS; out=$("$DEVENV" test --name "$NAME" 2>&1 | tail -3 | tr '\n' ' '); obs "test --name $NAME while up runs: in $((SECONDS - t))s: $out"
t=$SECONDS; out=$(cd cmd && "$DEVENV" redeploy 2>&1 | tail -2 | tr '\n' ' '); obs "redeploy from a subdirectory: $out"

section "F: Flux OCIRepository with the bundle's own layer media type"
curl -sSL -o "$W/flux-install.yaml" https://github.com/fluxcd/flux2/releases/latest/download/install.yaml
mk apply -f "$W/flux-install.yaml" >/dev/null
mk -n flux-system wait --for=condition=Available deploy --all --timeout=300s >/dev/null
ref=$(mk -n kapp-controller-packaging-global get packages.data.packaging.carvel.dev greeting-controller.demo.example.com.0.1.0 -o jsonpath='{.spec.template.spec.fetch[0].imgpkgBundle.image}')
mk apply -f - <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: greeting-controller, namespace: default}
spec:
  interval: 1m
  url: oci://${ref%@*}
  ref: {digest: ${ref#*@}}
  insecure: true
  layerSelector: {mediaType: application/vnd.docker.image.rootfs.diff.tar.gzip, operation: extract}
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
sleep 100
mk -n default get ocirepository,kustomization 2>&1
obs "OCIRepository: $(mk -n default get ocirepository greeting-controller -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>&1)"
obs "Kustomization: $(mk -n default get kustomization work-greeting-controller-flux -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>&1 | cut -c1-900)"

section "D: disk across redeploys, down, and purge"
obs "disk after up: $(df --output=used -BG / | tail -1)"
for i in 1 2 3 4 5 6; do
  CYC=d$i perl -pi -e 's/\(hello %s\)[^"]*"/(hello %s) $ENV{CYC}\\n"/' cmd/hello/main.go
  rd "d$i" >/dev/null
done
obs "disk after 6 redeploys: $(df --output=used -BG / | tail -1); dind volume and engine: $(docker system df --format '{{.Type}} {{.Size}}' 2>&1 | tr '\n' ' ')"
"$DEVENV" down --name "$NAME" >/dev/null 2>&1
obs "disk after down: $(df --output=used -BG / | tail -1)"
"$DEVENV" down --name "$NAME" --purge >/dev/null 2>&1
obs "disk after down --purge: $(df --output=used -BG / | tail -1)"
summary
