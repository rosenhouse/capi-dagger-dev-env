#!/usr/bin/env bash
# A Carvel team's addon-manager installs its agent package into workload clusters with a version constraint and values.
source "$(dirname "$0")/common.sh"
export DEVENV_SCENARIO=base
SCHEMA=$A/packages/addon-manager/bundle/config/schema.yml

remote_pkgi() {
  mk -n default get pkgi work-agent -o jsonpath='{.spec.packageRef.versionSelection.constraints} | {.status.friendlyDescription} | {.status.usefulErrorMessage}' 2>&1 | head -c 600; echo
}
agent_image() { wk -n acme-agent get deploy agent -o jsonpath='{.spec.template.spec.containers[0].image}' 2>&1 | head -c 200; }

section setup
setup_consumer

section "up with the production constraint >=1.0.0"
up_bg || { summary; exit 1; }
sed 's/^/    | /' "$W/up-$NAME.log" | tail -12

section "what a Carvel user inspects"
mk get pkgi -A
mk get packages.data.packaging.carvel.dev -A
obs "PackageMetadatas: $(mk get packagemetadatas -A --no-headers 2>&1 | wc -l) ($(mk get packagemetadatas -A 2>&1 | head -2 | tr '\n' ' '))"
obs "PackageRepositories: $(mk get packagerepositories -A 2>&1 | head -2 | tr '\n' ' ')"
obs "Package valuesSchema: '$(mk -n kapp-controller-packaging-global get packages.data.packaging.carvel.dev addon-manager.acme.example.com.0.1.0 -o jsonpath='{.spec.valuesSchema}' 2>&1)'"
obs "kapp-controller: $(mk -n kapp-controller get deploy kapp-controller -o jsonpath='{.metadata.annotations.kapp-controller\.carvel\.dev/version} {.spec.template.spec.containers[0].image}' 2>&1)"
mk get apps -A
echo "--- kctrl package available list -A"; kc package available list -A 2>&1 | tail -12
echo "--- kctrl package installed list -A"; kc package installed list -A 2>&1 | tail -12
echo "--- kctrl package available get --values-schema"; kc package available get -p addon-manager.acme.example.com/0.1.0 -n default --values-schema 2>&1 | tail -12
echo "--- kctrl package available get"; kc package available get -p addon-manager.acme.example.com -n default 2>&1 | tail -12
echo "--- kctrl package installed get"; kc package installed get -i addon-manager -n devenv 2>&1 | tail -15

section "namespace-less resources, overlay and README.md in config"
obs "acme-defaults ConfigMap (no namespace in config) landed in: $(mk get cm -A --field-selector metadata.name=acme-defaults -o jsonpath='{.items[*].metadata.namespace}')"
obs "overlay label on addon-manager Deployment: $(mk -n acme-system get deploy addon-manager -o jsonpath='{.metadata.labels}')"
obs "addon-manager App: $(mk -n devenv get app addon-manager -o jsonpath='{.status.friendlyDescription}')"

section "remote PackageInstall under the production constraint"
for i in $(seq 1 12); do mk -n default get pkgi work-agent >/dev/null 2>&1 && break; sleep 5; done
sleep 20
obs "after up, remote pkgi work-agent: $(remote_pkgi)"
obs "after up, agent Deployment in workload: $(wk -n acme-agent get deploy agent --no-headers 2>&1 | head -1)"

section "redeploy while no App installs the agent"
rd noapp
obs "after redeploy[noapp], remote pkgi: $(remote_pkgi)"

section "developer relaxes the constraint to >=0.0.0 in config and redeploys"
sed -i 's/constraint: ">=1.0.0"/constraint: ">=0.0.0"/' "$SCHEMA"
rd relax
obs "right after redeploy[relax]: App default/work-agent: $(mk -n default get app work-agent -o jsonpath='{.status.friendlyDescription}' 2>&1 | head -c 200); agent image in workload: $(agent_image)"
t0=$SECONDS
until wk -n acme-agent rollout status deploy/agent --timeout=5s >/dev/null 2>&1 || [ $((SECONDS - t0)) -gt 300 ]; do sleep 5; done
obs "agent became available in workload $((SECONDS - t0))s after redeploy[relax] returned; pkgi: $(remote_pkgi)"
obs "agent log (values Secret supplies clusterName): $(wk -n acme-agent logs deploy/agent 2>&1 | tail -1)"

section "change agent code and redeploy with --version 1.2.0: does redeploy wait for the workload side?"
before=$(agent_image)
sed -i 's/agent version=/agent v2 version=/' "$A/cmd/agent/main.go"
rd agentv2 --version 1.2.0
obs "agent image before: $before"
obs "agent image right after redeploy[agentv2]: $(agent_image)"
obs "agent log right after: $(wk -n acme-agent logs deploy/agent 2>&1 | tail -1)"
mk get packages.data.packaging.carvel.dev -A -o custom-columns=NAME:.metadata.name,VERSION:.spec.version
obs "Package versions after --version 1.2.0: $(mk get packages.data.packaging.carvel.dev -A -o jsonpath='{.items[*].spec.version}')"
obs "addon-manager log: $(mk -n acme-system logs deploy/addon-manager 2>&1 | head -1)"

section "drift: a Package deleted by hand, then redeploy with the same source and version"
mk -n kapp-controller-packaging-global delete packages.data.packaging.carvel.dev addon-manager.acme.example.com.0.1.0
rd drift --version 1.2.0
obs "Package after redeploy[drift]: $(mk -n kapp-controller-packaging-global get packages.data.packaging.carvel.dev addon-manager.acme.example.com.0.1.0 --no-headers 2>&1)"
obs "pkgi addon-manager after redeploy[drift]: $(mk -n devenv get pkgi addon-manager -o jsonpath='{.status.friendlyDescription}')"

section "addon-manager pins the agent to its own version (constraint: self) while the agent changes"
sed -i 's/constraint: ">=0.0.0"/constraint: "self"/' "$SCHEMA"
sed -i 's/agent v2 version=/agent v3 version=/' "$A/cmd/agent/main.go"
rd self --version 1.2.0
obs "after redeploy[self], remote pkgi: $(remote_pkgi)"
obs "after redeploy[self], App default/work-agent: $(mk -n default get app work-agent -o jsonpath='{.status.friendlyDescription}' 2>&1)"
obs "after redeploy[self], agent log: $(wk -n acme-agent logs deploy/agent 2>&1 | tail -1)"

section teardown
down_purge
summary
