#!/usr/bin/env bash
# Test hook: deploys the agent into the workload cluster with Flux, redeploys through the CLI, and checks the new build.
source "$TOOL/explore/flux/lib.sh"
AGENT=$(ref agentRef)
MANIFESTS=$(ref agentManifestsRef)
cat <<EOF | k apply -f - >/dev/null
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: agent, namespace: default}
spec:
  interval: 30s
  url: oci://${MANIFESTS%@*}
  ref: {digest: "${MANIFESTS#*@}"}
  insecure: true
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: agent-work, namespace: default}
spec:
  interval: 1m
  sourceRef: {kind: OCIRepository, name: agent}
  path: ./
  prune: true
  wait: true
  timeout: 3m
  kubeConfig: {secretRef: {name: work-kubeconfig}}
  images:
  - {name: agent, newName: "${AGENT%@*}", digest: "${AGENT#*@}"}
EOF
obs "Test hook Kustomization: $(ready ks agent-work 5m)"
obs "Test hook: agent serves $(served "$WORK" http://agent.agent:8080)"
t=$SECONDS
"$D" redeploy --version from-test-hook 2>&1 | tail -3
obs "Test hook: redeploy CLI from inside the test exit=${PIPESTATUS[0]} after $(since $t)s"
AGENT2=$(ref agentRef)
k -n default patch "$(kind_of ks)/agent-work" --type merge -p "{\"spec\":{\"images\":[{\"name\":\"agent\",\"newName\":\"${AGENT2%@*}\",\"digest\":\"${AGENT2#*@}\"}]}}" >/dev/null
flux --kubeconfig "$MGMT" -n default reconcile kustomization agent-work --timeout 3m >/dev/null 2>&1
v=$(served "$WORK" http://agent.agent:8080)
obs "Test hook: after repin agent serves $v"
[ "$v" = from-test-hook ]
