#!/usr/bin/env bash
# Usage: recover.sh identical|different
# After a redeploy with a broken package config, does fixing the config and redeploying recover?
# identical restores the last good config; different also adds a label, so the fixed bundle is new.
source "$(dirname "$0")/lib.sh"
MODE=$1
T=$RUNNER_TEMP
C=$T/rec
copy_repo "$C"; devenv_module "$C"; vendor_repo "$C/repo"
(cd "$C/repo" && echo '.devenv/' >>.gitignore && git commit -qam "ignore devenv state")
render "$C/repo" config/devenv deploy/devenv/rendered.yaml
cd "$C/repo/hack/devenv" || exit 1
export DEVENV_SCENARIO=rendered
if ! pid=$("$TOOL/explore/lib/up-bg.sh" "$T/up.log" 1200 -- "$C/devenv" up --name rec); then
  obs "up failed: $(grep -m1 '^Error' "$T/up.log")"; summary; exit 1
fi
export KUBECONFIG=$C/repo/hack/devenv/.devenv/rec/mgmt.kubeconfig
short() { sed 's/.*@sha256://' | cut -c1-8; }
snap() {
  echo "SNAP +$((SECONDS - s0))s package=$(kubectl get packages.data.packaging.carvel.dev -n kapp-controller-packaging-global fleet-addons.acme.io.0.1.0 -o jsonpath='{.spec.template.spec.fetch[0].imgpkgBundle.image}' | short)" \
    "pkgi=[$(kubectl -n devenv get pkgi fleet-addons -o jsonpath='{.status.friendlyDescription} gen={.metadata.generation} observed={.status.observedGeneration}')]" \
    "app=$(kubectl -n devenv get app fleet-addons -o jsonpath='{.spec.fetch[0].imgpkgBundle.image}' | short)" \
    "[$(kubectl -n devenv get app fleet-addons -o jsonpath='{.status.friendlyDescription} failures={.status.consecutiveReconcileFailures}')]"
}
s0=$SECONDS
snap
R=$C/repo/deploy/devenv/rendered.yaml
M=$C/repo/cmd/main.go
cp "$R" "$T/rendered.good"
cp "$M" "$T/main.good"
if [ "$MODE" = crash ]; then
  sed -i 's/^func main() {$/func main() {\n\tpanic("boom")/' "$M"
  grep -c 'panic("boom")' "$M"
else
  sed -i 's/^        image: cmd$/        image: cmdx/' "$R"
fi
start=$SECONDS
"$C/devenv" redeploy --name rec >"$T/bad.log" 2>&1
obs "$MODE: broken redeploy exit=$? after $((SECONDS - start))s: $(grep -m1 '^Error' "$T/bad.log" | cut -c1-300)"
snap
cp "$T/rendered.good" "$R"
cp "$T/main.good" "$M"
if [ "$MODE" = different ]; then
  sed -i '0,/^  labels:$/s//  labels:\n    acme.io\/touched: "yes"/' "$R"
  grep -c 'acme.io/touched' "$R"
fi
(while sleep 30; do snap; done) >"$T/snaps.log" 2>&1 &
snapper=$!
start=$SECONDS
"$C/devenv" redeploy --name rec >"$T/fix.log" 2>&1
obs "$MODE: redeploy after the fix exit=$? after $((SECONDS - start))s: $(grep -m1 '^Error' "$T/fix.log" | cut -c1-260)"
cat "$T/snaps.log"
for i in $(seq 1 16); do
  "$TOOL/explore/lib/pause.sh" 30; snap
  kubectl -n devenv get app fleet-addons -o jsonpath='{.status.friendlyDescription}' | grep -q 'Reconcile succeeded' && break
done
kill $snapper 2>/dev/null
obs "$MODE: $((SECONDS - s0))s after the bad redeploy started: $(snap)"
start=$SECONDS
"$C/devenv" redeploy --name rec >"$T/again.log" 2>&1
obs "$MODE: one more redeploy exit=$? after $((SECONDS - start))s: $(grep -m1 '^Error' "$T/again.log" | cut -c1-200)"
kubectl -n devenv get pkgi fleet-addons -o yaml | sed -n '/^status:/,$p' | head -40
stop_up "$pid"
summary
