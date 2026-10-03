#!/usr/bin/env bash
# Repeat run 1's capacity scenario: six extra clusters one at a time, then delete them all at once.
# Probe both host kubeconfigs for 10 minutes afterwards.
source "$(dirname "$0")/lib.sh"
install_tools
up cr || { summary; exit 1; }
probe() { # probe FILE KUBECONFIG
  while true; do
    if out=$(kubectl --kubeconfig "$2" --request-timeout=5s get --raw /readyz 2>&1); then r=ok; else r=$(echo "$out" | tail -1 | cut -c1-140); fi
    echo "$(date +%H:%M:%S) $(cat "$RUNNER_TEMP/phase") $r"
    sleep 3
  done >"$1" 2>&1
}
phase() { echo "$1" >"$RUNNER_TEMP/phase"; section "$1"; }
phase create
probe "$RUNNER_TEMP/mgmt-probe.log" "$MGMT" & P1=$!
probe "$RUNNER_TEMP/work-probe.log" "$WORK" & P2=$!
(while true; do echo "$(date +%H:%M:%S) $(cat "$RUNNER_TEMP/phase") load=$(cut -d' ' -f1 /proc/loadavg) $(free -m | awk '/Mem/{print "used="$3" avail="$7}')"; sleep 15; done >"$RUNNER_TEMP/res.log") & P3=$!
for i in 1 2 3 4 5 6; do
  new_cluster "extra$i" default v1.37.0 1 >/dev/null
  wait_until "extra$i Available" 720 cluster_available default "extra$i" || break
  wait_until "extra$i PackageInstall reconciled" 420 pkgi_reconciled default "extra$i-greeting-controller"
done
pause 20
phase delete-6
kubectl -n default delete cluster extra1 extra2 extra3 extra4 extra5 extra6 --wait=false
gone_all() { out=$(kubectl -n default get clusters --no-headers 2>&1) || return 1; ! echo "$out" | grep -q extra; }
wait_until "6 extra clusters deleted (API answering)" 600 gone_all
phase after
pause 240
phase redeploy
start=$(now)
(cd "$GREETING" && timeout 600 "$DEVENV" redeploy --name cr --version v2 >"$RUNNER_TEMP/rd.log" 2>&1); rc=$?
obs "redeploy --version v2 after deleting 6: exit $rc after $(since "$start")s; $(tail -1 "$RUNNER_TEMP/rd.log" | cut -c1-200)"
phase end
kill $P1 $P2 $P3
for f in mgmt work; do
  echo "--- $f probe outcomes per phase"
  awk '{p=$2; $1=$2=""; r=($0 ~ /^ *ok$/) ? "ok" : "fail"; n[p" "r]++} END {for (k in n) print k, n[k]}' "$RUNNER_TEMP/$f-probe.log" | sort
  awk '$3!="ok" {if (!($2 in a)) a[$2]=$1; b[$2]=$1} END {for (p in a) print p, "first fail", a[p], "last fail", b[p]}' "$RUNNER_TEMP/$f-probe.log"
  awk '{ $1=""; print }' "$RUNNER_TEMP/$f-probe.log" | grep -v ' ok$' | sort | uniq -c | sort -rn | head -5
done
echo "--- resources"; cat "$RUNNER_TEMP/res.log"
grep -E '^\[' "$RUNNER_TEMP/up-cr.log" | tail -4
down cr
summary
