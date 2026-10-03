#!/usr/bin/env bash
# Does the management API stay reachable from the host while several extra clusters are deleted at once?
# And how long does a real redeploy (new version) take with several clusters running the workload package?
source "$(dirname "$0")/lib.sh"
install_tools
up md || { summary; exit 1; }

probe() { # probe FILE KUBECONFIG
  while true; do
    if out=$(kubectl --kubeconfig "$2" --request-timeout=5s get --raw /readyz 2>&1); then r=ok; else r=$(echo "$out" | tail -1 | cut -c1-140); fi
    echo "$(date +%s) $(cat "$RUNNER_TEMP/phase") $r"
    sleep 3
  done >"$1" 2>&1
}
phase() { echo "$1" >"$RUNNER_TEMP/phase"; section "$1"; }
phase create
probe "$RUNNER_TEMP/mgmt-probe.log" "$MGMT" & P1=$!
probe "$RUNNER_TEMP/work-probe.log" "$WORK" & P2=$!
(while true; do echo "$(date +%s) $(cat "$RUNNER_TEMP/phase") load=$(cut -d' ' -f1 /proc/loadavg) $(free -m | awk '/Mem/{print "used="$3" avail="$7}')"; sleep 10; done >"$RUNNER_TEMP/res.log") & P3=$!

for i in 1 2 3 4; do new_cluster "extra$i" default v1.37.0 1 >/dev/null; done
all_available() { for i in 1 2 3 4; do cluster_available default "extra$i" || return 1; done; }
wait_until "4 extra clusters created at once all Available" 900 all_available || kubectl get clusters -A
apps_ok() { [ "$(kubectl get apps -A --no-headers | grep -c 'Reconcile succeeded')" = 7 ]; }
wait_until "7 Apps reconciled" 600 apps_ok; kubectl get apps -A

phase redeploy-5-clusters
start=$(now)
(cd "$GREETING" && timeout 900 "$DEVENV" redeploy --name md --version v2 >"$RUNNER_TEMP/rd.log" 2>&1); rc=$?
obs "redeploy --version v2 with 5 workload clusters: exit $rc after $(since "$start")s; $(tail -1 "$RUNNER_TEMP/rd.log")"
grep -E '^\[' "$RUNNER_TEMP/up-md.log" | tail -2
kubectl get apps -A

phase mass-delete
start=$(now)
kubectl -n default delete cluster extra1 extra2 extra3 extra4 --wait=false
gone_all() { out=$(kubectl -n default get clusters --no-headers 2>&1) || return 1; ! echo "$out" | grep -q extra; }
wait_until "4 extra clusters deleted (API answering)" 900 gone_all
phase after-delete
pause 120
phase redeploy-after-delete
start=$(now)
(cd "$GREETING" && timeout 900 "$DEVENV" redeploy --name md --version v3 >"$RUNNER_TEMP/rd.log" 2>&1); rc=$?
obs "redeploy --version v3 after mass delete: exit $rc after $(since "$start")s; $(tail -2 "$RUNNER_TEMP/rd.log" | tr '\n' ' ')"
phase end
kill $P1 $P2 $P3
for f in mgmt work; do
  echo "--- $f probe outcomes per phase"
  awk '{p=$2; $1=$2=""; r=($0 ~ /^ *ok$/) ? "ok" : "fail"; n[p" "r]++} END {for (k in n) print k, n[k]}' "$RUNNER_TEMP/$f-probe.log" | sort
  awk '{ $1=""; print }' "$RUNNER_TEMP/$f-probe.log" | grep -v ' ok$' | sort | uniq -c | sort -rn | head -6
  echo "--- $f probe failure windows (first and last failing timestamps per phase)"
  awk '$3!="ok" {if (!($2 in a)) a[$2]=$1; b[$2]=$1} END {for (p in a) print p, "first", a[p], "last", b[p], "span", b[p]-a[p]"s"}' "$RUNNER_TEMP/$f-probe.log"
done
echo "--- resources timeline"; cat "$RUNNER_TEMP/res.log"
grep -E '^\[' "$RUNNER_TEMP/up-md.log" | tail -8
down md
summary
