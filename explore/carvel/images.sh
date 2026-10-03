#!/usr/bin/env bash
# The team keeps its production image reference in config and builds it with an Images hook under that name.
# A vendored third-party package refers to images by tag, from mirrored and unmirrored registries.
source "$(dirname "$0")/common.sh"
NAME=img
export DEVENV_SCENARIO=prodrefs
SCHEMA=$A/packages/addon-manager/bundle/config/schema.yml

section setup
setup_consumer
sed -i 's|^image: addon-manager|image: ghcr.io/acme/addon-manager:v1.4.0|; s/constraint: ">=1.0.0"/constraint: ">=0.0.0"/' "$SCHEMA"
(cd "$A" && git commit -qam "dev: relax constraint")
cat "$SCHEMA"

section "up with production image refs and a tag-referenced third-party package"
if ! up_bg; then
  yq '.items[] | select(.kind == "App") | "App " + .metadata.namespace + "/" + .metadata.name + ": " + ((.status.usefulErrorMessage // .status.friendlyDescription // "") | tostring)' "$A/.devenv/$NAME/logs/resources.yaml" 2>/dev/null | head -c 3000
  summary; exit 1
fi

section "image references after kbld"
obs "addon-manager image: $(mk -n acme-system get deploy addon-manager -o jsonpath='{.spec.template.spec.containers[0].image}')"
obs "addon-manager ready: $(mk -n acme-system get deploy addon-manager -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')"
mk -n acme-third-party get deploy -o custom-columns=NAME:.metadata.name,IMAGE:.spec.template.spec.containers[0].image,READY:.status.readyReplicas
for d in mirrored-tag unmirrored-tag digest; do
  obs "third-party $d: image=$(mk -n acme-third-party get deploy $d -o jsonpath='{.spec.template.spec.containers[0].image}') ready=$(mk -n acme-third-party get deploy $d -o jsonpath='{.status.readyReplicas}')"
  mk -n acme-third-party get deploy $d -o jsonpath='{.metadata.annotations.kbld\.k14s\.io/images}'; echo
done
echo "--- third-party App template stderr"
mk -n devenv get app third-party -o jsonpath='{.status.template.stderr}' | tail -20
echo "--- third-party App fetch/template/deploy durations"
mk -n devenv get app third-party -o jsonpath='{.status.fetch.updatedAt} {.status.template.updatedAt} {.status.deploy.startedAt} {.status.deploy.finishedAt}'; echo

section "the images lock in a bundle with no first-party images"
obs "third-party pkgi: $(mk -n devenv get pkgi third-party -o jsonpath='{.status.friendlyDescription}')"

section "redeploy through the Images hook"
sed -i 's/addon-manager version=%s/addon-manager v2 version=%s/' "$A/cmd/addon-manager/main.go"
rd hook
obs "addon-manager image after redeploy: $(mk -n acme-system get deploy addon-manager -o jsonpath='{.spec.template.spec.containers[0].image}')"
sleep 15
obs "addon-manager log after redeploy: $(mk -n acme-system logs deploy/addon-manager 2>&1 | head -1)"

section "workload agent with values from the addon-manager"
t0=$SECONDS
until wk -n acme-agent rollout status deploy/agent --timeout=5s >/dev/null 2>&1 || [ $((SECONDS - t0)) -gt 240 ]; do sleep 5; done
obs "agent in workload after $((SECONDS - t0))s: $(wk -n acme-agent logs deploy/agent 2>&1 | tail -1)"

section teardown
down_purge
summary
