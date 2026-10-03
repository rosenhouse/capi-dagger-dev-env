#!/usr/bin/env bash
# Flux installed as a Management package; Flux deploys first-party builds into the workload cluster.
source "$(dirname "$0")/lib.sh"
setup_consumer
cd "$W" || exit 1
export FLUXCO_SCENARIO=hybrid

echo "=== What a rootfs made from a source subdirectory holds"
mkdir -p "$RUNNER_TEMP/rootfs"
GOWORK=off go run ./cmd/rootfsprobe "$RUNNER_TEMP/rootfs" 2>&1 | grep -v '^$' | tail -8
for tarball in "$RUNNER_TEMP"/rootfs/*.tar; do
  d=${tarball%.tar}; mkdir -p "$d"; tar -xf "$tarball" -C "$d"
  layers=$(jq -r '.[0].Layers[]' "$d/manifest.json")
  files=$(for l in $layers; do tar -tf "$d/$l"; done | grep -v '/$' | sort)
  obs "$(basename "$d") AsTarball: $(echo "$layers" | wc -w) layer(s), $(echo "$files" | wc -l) files: $(echo "$files" | head -6 | tr '\n' ' ')"
done

up hy || { summary; exit 1; }

echo "=== Flux install through kapp-controller"
k get apps -A
k -n flux-system get pods -o custom-columns=NAME:.metadata.name,READY:.status.containerStatuses[0].ready,IMAGE:.spec.containers[0].image
flux check --kubeconfig "$MGMT" 2>&1 | tail -12
obs "flux pods images: $(k -n flux-system get pods -o jsonpath='{range .items[*]}{.spec.containers[0].image}{" "}{end}' | tr ' ' '\n' | sed 's/@.*/@.../' | sort -u | tr '\n' ' ')"

echo "=== Which mirrors served Flux images"
podsh "$MGMT" certs "$BUSYBOX" sh '
for f in /hp/*/hosts.toml; do echo "== $f"; cat "$f"; done
m=$(grep -o "http://[^\"]*" /hp/ghcr.io/hosts.toml | head -1)
echo "ghcr mirror catalog:"; wget -qO- -T 10 "$m/v2/_catalog"; echo' "" /etc/containerd/certs.d

echo "=== Session registry host and refs, as consumer code can find them"
HOST=$(registry_host)
obs "registry host from Package CR: $HOST"
for key in agentRef managerRef agentManifestsRef managerManifestsRef agentChartRef; do obs "refs.$key = $(ref $key)"; done
AGENT=$(ref agentRef)
MANIFESTS=$(ref agentManifestsRef)
CHART=$(ref agentChartRef)
MANAGER=$(ref managerRef)
MGRMANIFESTS=$(ref managerManifestsRef)
obs "CAPI kubeconfig Secret keys: $(k -n default get secret work-kubeconfig -o jsonpath='{.data}' | jq -c keys) type $(k -n default get secret work-kubeconfig -o jsonpath='{.type}')"

echo "=== Media types of an Images-hook artifact"
podsh "$MGMT" crane "$CRANE" /busybox/sh "crane manifest --insecure $MANIFESTS; echo; crane ls --insecure ${MANIFESTS%@*}; crane ls --insecure ${AGENT%@*}; crane catalog --insecure $HOST" | tee "$RUNNER_TEMP/manifest.txt"
LAYER_MT=$(head -1 "$RUNNER_TEMP/manifest.txt" | jq -r '.layers[0].mediaType' 2>/dev/null)
obs "hook artifact manifest mediaType=$(head -1 "$RUNNER_TEMP/manifest.txt" | jq -r '.mediaType' 2>/dev/null) config=$(head -1 "$RUNNER_TEMP/manifest.txt" | jq -r '.config.mediaType' 2>/dev/null) layer=$LAYER_MT"

podsh "$MGMT" export "$CRANE" /busybox/sh "crane export --insecure $HOST/rootfs-probe - | tar -tf - | grep -v '/\$' | sort > /tmp/f; echo \$(wc -l < /tmp/f) files; head -6 /tmp/f" | tee "$RUNNER_TEMP/export.txt"
obs "rootfs-probe pushed by devenv holds: $(tr '\n' ' ' < "$RUNNER_TEMP/export.txt")"

echo "=== OCIRepository probes"
oci() { # NAME URLREF EXTRA
  local name=$1 r=$2 extra=$3
  cat <<EOF | k apply -f - >/dev/null
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: $name, namespace: default}
spec:
  interval: 30s
  url: oci://${r%@*}
  ref: {digest: "${r#*@}"}
$extra
EOF
}
oci om-insecure "$MANIFESTS" "  insecure: true"
obs "OCIRepository insecure, no layerSelector: $(ready oci om-insecure 120s)"

echo "=== Kustomization into the workload cluster through CAPI's kubeconfig Secret"
t=$SECONDS
cat <<EOF | k apply -f -
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: agent-work, namespace: default}
spec:
  interval: 1m
  sourceRef: {kind: OCIRepository, name: om-insecure}
  path: ./
  prune: true
  wait: true
  timeout: 3m
  kubeConfig: {secretRef: {name: work-kubeconfig}}
  images:
  - {name: agent, newName: "${AGENT%@*}", digest: "${AGENT#*@}"}
EOF
obs "Kustomization agent-work (workload): $(ready ks agent-work 5m) after $(since $t)s"
kw -n agent get deploy,pods -o wide
V1=$(served "$WORK" http://agent.agent:8080)
obs "agent serves: $V1"

echo "=== Kustomization of the management controller, tag-based"
cat <<EOF | k apply -f -
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata: {name: mgr-latest, namespace: default}
spec:
  interval: 30s
  url: oci://${MGRMANIFESTS%@*}
  ref: {tag: latest}
  insecure: true
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: manager-mgmt, namespace: default}
spec:
  interval: 1m
  sourceRef: {kind: OCIRepository, name: mgr-latest}
  path: ./
  prune: true
  wait: true
  timeout: 3m
  images:
  - {name: manager, newName: "${MANAGER%@*}", newTag: latest}
EOF
obs "Kustomization manager-mgmt (mgmt, tag latest): $(ready ks manager-mgmt 4m)"
M1=$(served "$MGMT" http://manager.manager:8080)
obs "manager serves: $M1"

echo "=== Redeploy"
k get apps -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,FETCH:.spec.fetch[0].imgpkgBundle.image,DEPLOYED:.status.deploy.startedAt
FLUXPODS_BEFORE=$(k -n flux-system get pods -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')
t=$SECONDS
"$D" redeploy --name hy --version v2 2>&1 | tail -8
rc=${PIPESTATUS[0]}
obs "redeploy --version v2 exit=$rc took $(since $t)s"
k get apps -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,FETCH:.spec.fetch[0].imgpkgBundle.image,DEPLOYED:.status.deploy.startedAt
obs "flux pods unchanged by redeploy: $([ "$FLUXPODS_BEFORE" = "$(k -n flux-system get pods -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')" ] && echo yes || echo no)"
obs "refs.agentRef after redeploy changed: $([ "$(ref agentRef)" != "$AGENT" ] && echo yes || echo no); agentManifestsRef changed: $([ "$(ref agentManifestsRef)" != "$MANIFESTS" ] && echo yes || echo no)"
obs "right after redeploy: agent serves $(served "$WORK" http://agent.agent:8080); manager serves $(served "$MGMT" http://manager.manager:8080)"
for o in "oci om-insecure" "ks agent-work" "oci mgr-latest" "ks manager-mgmt"; do obs "  $o revision: $(revision $o)"; done
"$TOOL/explore/lib/pause.sh" 90
obs "90s after redeploy: agent serves $(served "$WORK" http://agent.agent:8080); manager serves $(served "$MGMT" http://manager.manager:8080)"
k -n manager get pods -o wide

echo "=== Workaround: repin Flux objects to the new refs and reconcile"
t=$SECONDS
AGENT2=$(ref agentRef)
MANIFESTS2=$(ref agentManifestsRef)
k -n default patch "$(kind_of oci)/om-insecure" --type merge -p "{\"spec\":{\"ref\":{\"digest\":\"${MANIFESTS2#*@}\"}}}" >/dev/null
k -n default patch "$(kind_of ks)/agent-work" --type merge -p "{\"spec\":{\"images\":[{\"name\":\"agent\",\"newName\":\"${AGENT2%@*}\",\"digest\":\"${AGENT2#*@}\"}]}}" >/dev/null
flux --kubeconfig "$MGMT" -n default reconcile kustomization agent-work --with-source --timeout 3m 2>&1 | tail -2
kw -n agent rollout status deploy/agent --timeout=2m >/dev/null
obs "after repin + flux reconcile + rollout ($(since $t)s): agent serves $(served "$WORK" http://agent.agent:8080)"
k -n manager rollout restart deploy/manager >/dev/null && k -n manager rollout status deploy/manager --timeout=2m >/dev/null
obs "after rollout restart: manager serves $(served "$MGMT" http://manager.manager:8080)"

echo "=== Teardown"
"$D" status
down hy
summary
