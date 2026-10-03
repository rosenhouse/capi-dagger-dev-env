#!/usr/bin/env bash
# 1. A management package with a required value: can the user supply it by patching the PackageInstall while up waits?
# 2. Config that carries an older images lock (bundle root, committed kbld lock), and a Helm chart.
source "$(dirname "$0")/common.sh"
NAME=vals

# start_up SCENARIO starts up in the background and sets UP_PID.
start_up() {
  : >"$W/up-$1.log"
  (cd "$A" && DEVENV_SCENARIO=$1 timeout 1500 "$DEVENV" up --name "$NAME") >"$W/up-$1.log" 2>&1 &
  UP_PID=$!
  T_UP=$SECONDS
}
# wait_for CMD... polls CMD for up to 10 minutes while up runs.
wait_for() {
  local t0=$SECONDS
  until "$@" >/dev/null 2>&1; do
    kill -0 "$UP_PID" 2>/dev/null || return 1
    [ $((SECONDS - t0)) -gt 600 ] && return 1
    sleep 3
  done
}
# finish_up LABEL waits for up to report "is up." or exit.
finish_up() {
  while kill -0 "$UP_PID" 2>/dev/null && ! grep -q " is up\." "$W/up-$1.log"; do sleep 3; done
  if grep -q " is up\." "$W/up-$1.log"; then obs "up[$1] is UP after $((SECONDS - T_UP))s"; return 0; fi
  wait "$UP_PID"; obs "up[$1] exited rc=$? after $((SECONDS - T_UP))s"
  sed 's/^/    | /' "$W/up-$1.log" | tail -25
  return 1
}

section setup
setup_consumer

section "required value: patch the PackageInstall with a values Secret during up's gate"
start_up workaround
if wait_for mk -n devenv get pkgi dns-config; then
  obs "pkgi dns-config appeared $((SECONDS - T_UP))s into up"
  sleep 10
  obs "dns-config before patch: $(mk -n devenv get pkgi dns-config -o jsonpath='{.status.friendlyDescription}')"
  mk -n devenv create secret generic dns-config-values --from-literal=values.yml='clusterDomain: acme.internal'
  mk -n devenv patch pkgi dns-config --type merge -p '{"spec":{"values":[{"secretRef":{"name":"dns-config-values"}}]}}'
else
  obs "pkgi dns-config never appeared"
fi
if finish_up workaround; then
  obs "acme-dns ConfigMap: $(mk -n default get cm acme-dns -o jsonpath='{.data}')"
  sed -i 's/addon-manager version=%s/addon-manager v2 version=%s/' "$A/cmd/addon-manager/main.go"
  rd keeps-patch
  obs "after redeploy, dns-config values: $(mk -n devenv get pkgi dns-config -o jsonpath='{.spec.values}') $(mk -n devenv get pkgi dns-config -o jsonpath='{.status.friendlyDescription}')"
  (cd "$A" && "$DEVENV" down --name "$NAME") >/dev/null 2>&1
  wait "$UP_PID"
fi

section "older images locks and a Helm chart in config"
start_up locks
if wait_for mk -n devenv get app kbld-locked; then
  sleep 60
  for ns in acme-legacy acme-kbld; do
    obs "$ns images: $(mk -n $ns get deploy -o jsonpath='{range .items[*]}{.metadata.name}={.spec.template.spec.containers[0].image} {end}' 2>&1)"
    mk -n $ns get pods --no-headers 2>&1 | head -3
  done
  for app in legacy-lock kbld-locked helm-chart; do
    obs "App $app: $(mk -n devenv get app $app -o jsonpath='{.status.friendlyDescription} {.status.usefulErrorMessage}' 2>&1 | head -c 600)"
    mk -n devenv get app $app -o jsonpath='{.status.template.stderr}' 2>&1 | tail -8
  done
fi
finish_up locks

section teardown
down_purge
summary
