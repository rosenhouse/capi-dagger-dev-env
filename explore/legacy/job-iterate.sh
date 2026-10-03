#!/usr/bin/env bash
# The inner loop a converted team lives in: kubebuilder's command: /manager kept by mistake,
# no-op redeploys with .devenv/ ignored, a broken config redeploy and its recovery.
source "$(dirname "$0")/lib.sh"
T=$RUNNER_TEMP
C=$T/iter
copy_repo "$C"; devenv_module "$C"; vendor_repo "$C/repo"
(cd "$C/repo" && echo '.devenv/' >>.gitignore && git commit -qam "ignore devenv state")
render "$C/repo" config/devenv deploy/devenv/rendered.yaml
render "$C/repo" config/devenv-keepcmd deploy/keepcmd/rendered.yaml
cd "$C/repo/hack/devenv" || exit 1

echo "### A: Commands-built images with kubebuilder's command: /manager"
DEVENV_SCENARIO=keepcmd expect_fail "$T/keepcmd.log" 1200 -- "$C/devenv" up --name iter
state_tail "$C/repo/hack/devenv" iter
grep -rhoE '(exec|stat) [^"]*/manager[^"]*' "$C/repo/hack/devenv/.devenv/iter/logs" 2>/dev/null | sort | uniq -c | head -3
grep -rh 'fleet-addons-controller-manager' "$C/repo/hack/devenv/.devenv/iter/logs/resources.yaml" 2>/dev/null | head -3

echo "### B: rendered config with .devenv/ ignored"
export DEVENV_SCENARIO=rendered
start=$SECONDS
if ! pid=$("$TOOL/explore/lib/up-bg.sh" "$T/up.log" 1500 -- "$C/devenv" up --name iter); then
  obs "B: up failed: $(grep -m1 '^Error' "$T/up.log" | cut -c1-300)"; summary; exit 1
fi
obs "B: up took $((SECONDS - start))s (second up with this name)"
export KUBECONFIG=$C/repo/hack/devenv/.devenv/iter/mgmt.kubeconfig
bundles() { kubectl get apps -A -o jsonpath='{range .items[*]}{.spec.fetch[0].imgpkgBundle.image}{"\n"}{end}' | grep fleet; }
pods() { kubectl -n fleet-addons-system get pods -o jsonpath='{.items[*].metadata.name}'; }
kubectl -n fleet-addons-system rollout status deploy/fleet-addons-controller-manager --timeout=60s >/dev/null
b0=$(bundles); p0=$(pods)
for i in 1 2; do
  start=$SECONDS
  "$C/devenv" redeploy --name iter 2>&1 | tail -1
  obs "B: no-op redeploy #$i with .devenv/ ignored took $((SECONDS - start))s; bundle changed: $([ "$b0" != "$(bundles)" ] && echo yes || echo no); manager pod replaced: $([ "$p0" != "$(pods)" ] && echo yes || echo no)"
done
grep 'OBS: Images hook' "$T/up.log"

echo "### C: a typo in the package config, then the fix"
R=$C/repo/deploy/devenv/rendered.yaml
cp "$R" "$T/rendered.good"
sed -i 's/^        image: cmd$/        image: cmdx/' "$R"
grep -c 'image: cmdx' "$R"
start=$SECONDS
"$C/devenv" redeploy --name iter >"$T/bad-redeploy.log" 2>&1
obs "C: redeploy with image: cmdx exit=$? after $((SECONDS - start))s: $(grep -m1 -E '^Error' "$T/bad-redeploy.log" | cut -c1-400)"
tail -8 "$T/bad-redeploy.log"
obs "C: App status right after: $(kubectl -n devenv get app fleet-addons -o jsonpath='{.status.friendlyDescription}')"
cp "$T/rendered.good" "$R"
start=$SECONDS
"$C/devenv" redeploy --name iter >"$T/fix-redeploy.log" 2>&1
obs "C: redeploy after the fix exit=$? after $((SECONDS - start))s: $(tail -1 "$T/fix-redeploy.log")"

echo "### D: a Go syntax error, then the fix"
echo 'func broken() {' >>"$C/repo/internal/version/version.go"
start=$SECONDS
"$C/devenv" redeploy --name iter >"$T/syntax.log" 2>&1
obs "D: redeploy with a syntax error exit=$? after $((SECONDS - start))s: $(grep -m1 '^Error' "$T/syntax.log" | cut -c1-300)"
tail -6 "$T/syntax.log"
git -C "$C/repo" checkout -q -- internal/version/version.go
start=$SECONDS
"$C/devenv" redeploy --name iter >"$T/syntaxfix.log" 2>&1
obs "D: redeploy after the fix exit=$? after $((SECONDS - start))s"

stop_up "$pid"
summary
