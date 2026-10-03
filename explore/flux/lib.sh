#!/usr/bin/env bash
# Shared helpers for the Flux exploration jobs. Source it.
TOOL=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
FLUX_VERSION=2.9.6
D=$RUNNER_TEMP/devenv
W=$RUNNER_TEMP/fluxco
BUSYBOX=busybox:1.37
CRANE=gcr.io/go-containerregistry/crane:debug@sha256:e78770b31258a3846f878036d9c1f63fbe4c871f9f56990bf77fd95c013e3c1b
SUMMARY=()
obs() { echo "OBS: $*"; SUMMARY+=("$*"); }
summary() { echo "===== SUMMARY ($0) ====="; printf '  %s\n' "${SUMMARY[@]}"; }
since() { echo $((SECONDS - $1)); }

setup_consumer() {
  "$TOOL/explore/lib/adopter.sh" "$TOOL/explore/flux/consumer" "$W" >/dev/null
  rm -f "$W/config/flux/placeholder.txt"
  curl -sSfL -o "$W/config/flux/install.yaml" "https://github.com/fluxcd/flux2/releases/download/v$FLUX_VERSION/install.yaml"
  (cd "$W" && GOWORK=off go build -o "$D" ./cmd/devenv) || { echo "consumer build failed"; exit 1; }
  curl -sSfL "https://github.com/fluxcd/flux2/releases/download/v$FLUX_VERSION/flux_${FLUX_VERSION}_linux_amd64.tar.gz" | sudo tar -xz -C /usr/local/bin flux
  flux version --client
}

# up NAME: brings up an environment in the background and exports MGMT and WORK.
up() {
  local t=$SECONDS
  if ! UP_PID=$("$TOOL/explore/lib/up-bg.sh" "$RUNNER_TEMP/up-$1.log" 1500 -- "$D" up --name "$1"); then
    obs "up $1 FAILED after $(since $t)s"
    dump "$1"
    return 1
  fi
  obs "up $1 took $(since $t)s"
  grep -E '^\[' "$RUNNER_TEMP/up-$1.log"
  export MGMT=$W/.devenv/$1/mgmt.kubeconfig WORK=$W/.devenv/$1/workload.kubeconfig
}

# down NAME: stops the environment and waits for its up process.
down() {
  local t=$SECONDS
  "$D" down --name "$1" 2>&1 | tail -3
  while kill -0 "$UP_PID" 2>/dev/null && [ $((SECONDS - t)) -lt 180 ]; do sleep 2; done
  echo "up process gone after $(since $t)s; last lines:"
  tail -3 "$RUNNER_TEMP/up-$1.log"
}

dump() {
  echo "--- tail of up log"; tail -40 "$RUNNER_TEMP/up-$1.log"
  ls -R "$W/.devenv/$1" 2>/dev/null | head -40
  grep -h -B2 -A6 'usefulErrorMessage' "$W"/.devenv/"$1"/logs/resources.yaml 2>/dev/null | head -80
}

k() { kubectl --kubeconfig "$MGMT" "$@"; }
kw() { kubectl --kubeconfig "$WORK" "$@"; }

# registry_host reads the session registry's host from a Package's bundle reference.
registry_host() {
  k get packages.data.packaging.carvel.dev -A -o jsonpath='{.items[0].spec.template.spec.fetch[0].imgpkgBundle.image}' | cut -d/ -f1
}

# ref KEY reads a kbld-resolved reference from the refs package's ConfigMap.
ref() { k -n devenv-refs get cm refs -o jsonpath="{.data.$1}"; }

# podrun KUBECONFIG NAME IMAGE CMD...: runs a pod to completion in default and prints its logs.
podrun() {
  local kc=$1 name=$2 image=$3
  shift 3
  kubectl --kubeconfig "$kc" -n default delete pod "$name" --ignore-not-found --wait=true >/dev/null 2>&1
  kubectl --kubeconfig "$kc" -n default run "$name" --image="$image" --restart=Never --command -- "$@" >/dev/null
  waitpod "$kc" "$name"
}

# podsh KUBECONFIG NAME IMAGE SHELL SCRIPT [CONFIGMAP [HOSTPATH]]: runs SCRIPT in a pod, with CONFIGMAP at /in and HOSTPATH at /hp.
podsh() {
  local kc=$1 name=$2 image=$3 shell=$4 script=$5 cm=${6:-} hp=${7:-}
  kubectl --kubeconfig "$kc" -n default delete pod "$name" --ignore-not-found --wait=true >/dev/null 2>&1
  jq -n --arg n "$name" --arg i "$image" --arg sh "$shell" --arg s "$script" --arg cm "$cm" --arg hp "$hp" '{
    apiVersion: "v1", kind: "Pod", metadata: {name: $n, namespace: "default"},
    spec: {restartPolicy: "Never",
      containers: [{name: "c", image: $i, command: [$sh, "-c", $s],
        volumeMounts: ([if $cm != "" then {name: "in", mountPath: "/in"} else empty end] + [if $hp != "" then {name: "hp", mountPath: "/hp"} else empty end])}],
      volumes: ([if $cm != "" then {name: "in", configMap: {name: $cm}} else empty end] + [if $hp != "" then {name: "hp", hostPath: {path: $hp}} else empty end])}}' |
    kubectl --kubeconfig "$kc" apply -f - >/dev/null
  waitpod "$kc" "$name"
}

waitpod() {
  local kc=$1 name=$2 phase end
  end=$((SECONDS + 180))
  while [ $SECONDS -lt $end ]; do
    phase=$(kubectl --kubeconfig "$kc" -n default get pod "$name" -o jsonpath='{.status.phase}' 2>/dev/null)
    case $phase in Succeeded|Failed) break ;; esac
    sleep 3
  done
  [ "$phase" = Succeeded ] || echo "(pod $name phase=$phase)"
  kubectl --kubeconfig "$kc" -n default logs "$name" 2>&1 | tail -30
}

# served KUBECONFIG URL prints what a Service serves.
served() { podrun "$1" "get-$RANDOM" "$BUSYBOX" wget -qO- -T 5 "$2"; }

FLUXKINDS="ocirepositories.source.toolkit.fluxcd.io kustomizations.kustomize.toolkit.fluxcd.io helmreleases.helm.toolkit.fluxcd.io helmrepositories.source.toolkit.fluxcd.io"
kind_of() {
  case $1 in
    oci) echo ocirepositories.source.toolkit.fluxcd.io ;;
    ks) echo kustomizations.kustomize.toolkit.fluxcd.io ;;
    hr) echo helmreleases.helm.toolkit.fluxcd.io ;;
    hrepo) echo helmrepositories.source.toolkit.fluxcd.io ;;
  esac
}

# ready KIND NAME TIMEOUT: waits for a Flux object in default to be Ready and prints its Ready condition.
ready() {
  local kind rc
  kind=$(kind_of "$1")
  k -n default wait "$kind/$2" --for=condition=Ready --timeout="$3" >/dev/null 2>&1
  rc=$?
  k -n default get "$kind/$2" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status} {.reason}: {.message}{end}' | head -c 600
  echo
  return $rc
}

# revision KIND NAME prints a Flux object's last applied or artifact revision.
revision() {
  k -n default get "$(kind_of "$1")/$2" -o jsonpath='{.status.lastAppliedRevision}{.status.artifact.revision}' 2>/dev/null
}
