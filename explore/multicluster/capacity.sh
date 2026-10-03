#!/usr/bin/env bash
# How many extra workload clusters fit beside the greeting environment on a 4-CPU/16-GB runner?
source "$(dirname "$0")/lib.sh"
install_tools
echo "runner: $(nproc) CPUs, $(free -g | awk '/Mem/{print $2}') GB"
resources
up cap || { summary; exit 1; }
resources
docker stats --no-stream --format '{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}'
base=$(free -m | awk '/Mem/{print $3}')
obs "after up: $(free -m | awk '/Mem/{print $3}')MiB used"

fitted=0
for i in 1 2 3 4 5 6; do
  section "extra$i"
  start=$(now)
  new_cluster "extra$i" default v1.37.0 1 >/dev/null
  if ! wait_until "extra$i Available" 720 cluster_available default "extra$i"; then
    dump default "extra$i"
    resources
    break
  fi
  avail=$(since "$start")
  wait_until "extra$i greeting-controller PackageInstall reconciled" 420 pkgi_reconciled default "extra$i-greeting-controller"
  fitted=$i
  pause 20
  obs "extra$i: Available in ${avail}s; $(free -m | awk '/Mem/{print $3}')MiB used ($(( $(free -m | awk '/Mem/{print $3}') - base ))MiB over base), load $(cut -d' ' -f1 /proc/loadavg)"
  if ! kubectl --kubeconfig "$WORK" --request-timeout=10s get --raw /readyz >/dev/null 2>&1; then obs "work API from host failing with $i extras"; fi
  if [ "$(free -m | awk '/Mem/{print $7}')" -lt 1500 ]; then obs "stopping: under 1.5 GiB available"; break; fi
done
obs "extra clusters that came up: $fitted"
kubectl get clusters -A
docker stats --no-stream --format '{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}'
redeploy cap "with $fitted extra clusters"
kubectl get apps -A
start=$(now)
for i in $(seq 1 "$fitted"); do kubectl -n default delete cluster "extra$i" --wait=false; done
gone_all() { [ "$(kubectl -n default get clusters --no-headers | grep -c extra)" = 0 ]; }
wait_until "extra clusters deleted" 900 gone_all
resources
redeploy cap "after deleting extras"
down cap
summary
