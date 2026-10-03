#!/usr/bin/env bash
# A Flux-only team: no Carvel packages of its own, Flux installed with the flux CLI, plain kustomize for the manager.
source "$(dirname "$0")/lib.sh"
setup_consumer
cd "$W" || exit 1

echo "=== No packages"
out=$(FLUXCO_SCENARIO=nopkg "$D" up --name np 2>&1)
obs "up with Commands and Images but no Packages: exit=$? output: $(echo "$out" | tr '\n' ' ' | head -c 300)"

echo "=== A placeholder Management package"
export FLUXCO_SCENARIO=fluxonly
up fo || { summary; exit 1; }

echo "=== flux install from the host"
t=$SECONDS
flux install --kubeconfig "$MGMT" --timeout 6m 2>&1 | tail -6
obs "flux install exit=${PIPESTATUS[0]} took $(since $t)s"
flux check --kubeconfig "$MGMT" 2>&1 | tail -4

HOST=$(registry_host)
obs "registry host from Package CR: $HOST"
podsh "$MGMT" crane "$CRANE" /busybox/sh "crane catalog --insecure $HOST; for r in agent manager agent-manifests manager-manifests agent-chart; do echo \"\$r tags: \$(crane ls --insecure $HOST/\$r | tr '\n' ' ')\"; done"

echo "=== Agent through Flux, tag-based"
cat <<EOF | k apply -f -
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: agent-latest, namespace: default}
spec:
  interval: 30s
  url: oci://$HOST/agent-manifests
  ref: {tag: latest}
  insecure: true
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: agent-work, namespace: default}
spec:
  interval: 1m
  sourceRef: {kind: OCIRepository, name: agent-latest}
  path: ./
  prune: true
  wait: true
  timeout: 3m
  kubeConfig: {secretRef: {name: work-kubeconfig}}
  images:
  - {name: agent, newName: "$HOST/agent", newTag: latest}
EOF
t=$SECONDS
obs "Kustomization agent-work: $(ready ks agent-work 5m) after $(since $t)s"
obs "agent serves: $(served "$WORK" http://agent.agent:8080)"

echo "=== Manager with plain kustomize from the host"
mkdir -p "$RUNNER_TEMP/overlay"
cat > "$RUNNER_TEMP/overlay/kustomization.yaml" <<EOF
resources:
- $(realpath --relative-to="$RUNNER_TEMP/overlay" "$W/deploy/manager")
images:
- {name: manager, newName: "$HOST/manager", newTag: latest}
EOF
k apply -k "$RUNNER_TEMP/overlay" && k -n manager rollout status deploy/manager --timeout=3m
obs "manager serves: $(served "$MGMT" http://manager.manager:8080)"

echo "=== Redeploy"
t=$SECONDS
"$D" redeploy --name fo --version v2 2>&1 | tail -6
rc=${PIPESTATUS[0]}
obs "redeploy --version v2 exit=$rc took $(since $t)s"
obs "right after redeploy: agent serves $(served "$WORK" http://agent.agent:8080); manager serves $(served "$MGMT" http://manager.manager:8080); OCIRepository revision $(revision oci agent-latest)"
"$TOOL/explore/lib/pause.sh" 90
obs "90s later: agent serves $(served "$WORK" http://agent.agent:8080); manager serves $(served "$MGMT" http://manager.manager:8080); OCIRepository revision $(revision oci agent-latest)"
t=$SECONDS
kw -n agent rollout restart deploy/agent >/dev/null && kw -n agent rollout status deploy/agent --timeout=2m >/dev/null
k -n manager rollout restart deploy/manager >/dev/null && k -n manager rollout status deploy/manager --timeout=2m >/dev/null
obs "after rollout restarts ($(since $t)s): agent serves $(served "$WORK" http://agent.agent:8080); manager serves $(served "$MGMT" http://manager.manager:8080)"

echo "=== Redeploy with an edited manifest"
R0=$(revision oci agent-latest)
sed -i 's/containerPort: 8080/containerPort: 8080\n          name: http/' "$W/deploy/agent/agent.yaml"
t=$SECONDS
"$D" redeploy --name fo --version v3 2>&1 | tail -3
obs "redeploy v3 after a manifest edit took $(since $t)s; OCIRepository revision changed by then: $([ "$(revision oci agent-latest)" != "$R0" ] && echo yes || echo no)"
t=$SECONDS
while [ $((SECONDS - t)) -lt 180 ]; do
  R1=$(revision oci agent-latest)
  [ "$R1" != "$R0" ] && [ "$(revision ks agent-work)" = "$R1" ] && break
  sleep 3
done
obs "Flux applied the edited manifests $(since $t)s after redeploy returned (OCIRepository $R0 -> $(revision oci agent-latest), Kustomization $(revision ks agent-work))"
kw -n agent rollout status deploy/agent --timeout=2m >/dev/null
obs "after manifest-edit redeploy: agent serves $(served "$WORK" http://agent.agent:8080)"

echo "=== Teardown"
"$D" status
down fo
summary
