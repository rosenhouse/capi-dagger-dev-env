#!/usr/bin/env bash
# Ready hook: installs Flux on the management cluster from the host.
t=$SECONDS
kubectl --kubeconfig "$MGMT" apply --server-side -f config/flux/install.yaml >/dev/null || exit 1
kubectl --kubeconfig "$MGMT" -n flux-system wait deploy --all --for=condition=Available --timeout=5m >/dev/null || exit 1
echo "OBS: Ready hook installed Flux in $((SECONDS - t))s"
