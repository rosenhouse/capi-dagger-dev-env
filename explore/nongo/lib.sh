# Shared helpers for the nongo exploration scripts. Source it; it expects T, DEVENV, NAME, STATE.
obs() { echo "OBS: $*"; SUMMARY+=("$*"); }
summary() {
  echo "================ SUMMARY ================"
  for s in "${SUMMARY[@]}"; do echo "- $s"; done
}
kmgmt() { kubectl --kubeconfig "$STATE/$NAME/mgmt.kubeconfig" "$@"; }
img() { kmgmt -n "$1" get deploy "$2" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>&1; }
same() { if [ "$1" = "$2" ]; then echo same; else echo CHANGED; fi; }
# redeploy ARGS... runs devenv redeploy, records rc and duration in RD_RC and RD_SECS, output in $T/rd.out.
redeploy() {
  local t=$SECONDS
  "$DEVENV" redeploy --name "$NAME" "$@" >"$T/rd.out" 2>&1
  RD_RC=$?; RD_SECS=$((SECONDS - t))
  echo "--- redeploy $* (rc=$RD_RC, ${RD_SECS}s) output:"
  tail -40 "$T/rd.out"
  echo "---"
}
dump_state() {
  echo "--- tail of $STATE/$NAME files"
  ls -la "$STATE/$NAME" 2>&1
  tail -60 "$STATE/$NAME/dagger.log" 2>&1
}
