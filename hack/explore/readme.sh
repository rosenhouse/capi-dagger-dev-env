#!/bin/bash
# The README's instructions exactly as written, on a runner with only KVM and Go set up.
source hack/explore/lib.sh

say "Install smolvm as the README says"
curl -fsSL https://raw.githubusercontent.com/smol-machines/smolvm/v1.22.0/scripts/install.sh | bash -s -- --version 1.22.0
echo "[install exit $?]"
export PATH=$HOME/.local/bin:$PATH
run which smolvm
run smolvm --version
ls -la ~/.smolvm ~/.local/bin
env | grep -i smolvm || true

# vmm_cpu prints each VM's VMM CPU use over 60 s, as a fraction of one core. smolvm's idle reclaim needs it below 0.01.
vmm_cpu() {
  local hz; hz=$(getconf CLK_TCK)
  declare -A t0
  local pids; pids=$(smolvm machine ls --json | jq -r '.[].pid // empty')
  for p in $pids; do t0[$p]=$(sudo awk '{ print $14 + $15 }' "/proc/$p/stat"); done
  sleep 60
  for p in $pids; do
    echo "VMM $p: $(sudo awk -v t0="${t0[$p]}" -v hz="$hz" '{ printf "%.4f", ($14 + $15 - t0) / hz / 60 }' "/proc/$p/stat") of a core"
  done
}

say "README: up"
run go run ./cmd/devenv up
vmm_cpu
say "README: test"
run go run ./cmd/devenv test
say "README: redeploy"
run go run ./cmd/devenv redeploy
say "README: status"
run go run ./cmd/devenv status
say "README: kubeconfig"
go run ./cmd/devenv kubeconfig --cluster workload > workload.kubeconfig
echo "[exit $?]"
run kubectl --kubeconfig workload.kubeconfig get nodes
say "README: down --purge"
run go run ./cmd/devenv down --purge
machines; ls -la .devenv; git status --porcelain

say "up twice without --name"
run go run ./cmd/devenv up
run go run ./cmd/devenv up
run go run ./cmd/devenv status
run go run ./cmd/devenv redeploy
run go run ./cmd/devenv kubeconfig
run go run ./cmd/devenv down
free -m
machines
for env in $(ls .devenv); do run go run ./cmd/devenv down --purge --name "$env"; done
machines
