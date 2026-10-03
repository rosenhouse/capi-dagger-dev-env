#!/usr/bin/env bash
# Helpers for the diagnosis scenarios. Source after setting REPO.
SRC=$RUNNER_TEMP/diag-src
DIAG=$RUNNER_TEMP/diag
SUMMARY=$RUNNER_TEMP/summary.txt
: >"$SUMMARY"

obs() { echo "OBS: $*"; echo "$*" >>"$SUMMARY"; }

setup() {
  local t=$SECONDS
  "$REPO/explore/lib/adopter.sh" "$REPO/explore/diagnosis/consumer" "$SRC" >/dev/null 2>&1 || { echo "adopter failed"; "$REPO/explore/lib/adopter.sh" "$REPO/explore/diagnosis/consumer" "$SRC"; }
  (cd "$SRC" && GOWORK=off go build -o "$DIAG" ./cmd/devenv) || echo "build failed"
  obs "setup took $((SECONDS - t))s"
}

# run_case LABEL TIMEOUT [VAR=VALUE...] -- ARGS... runs the consumer's devenv in $SRC.
run_case() {
  local label=$1 timeout=$2; shift 2
  local envs=()
  while [ "$1" != "--" ]; do envs+=("$1"); shift; done
  shift
  touch "$RUNNER_TEMP/stamp-$label"
  local out=$RUNNER_TEMP/out-$label.log t=$SECONDS
  echo "::group::$label: ${envs[*]} devenv $*"
  (cd "${RUN_DIR:-$SRC}" && env "${envs[@]}" timeout --signal=INT --kill-after=60 "$timeout" "${RUN_BIN:-$DIAG}" "$@") >"$out" 2>&1
  local code=$?
  echo "::endgroup::"
  obs "[$label] exit=$code after $((SECONDS - t))s (${envs[*]} $*)"
  obs "[$label] output lines=$(wc -l <"$out")"
  echo "----- [$label] last 40 lines of output"
  grep -v '^\s*$' "$out" | tail -40
  echo "-----"
}

# inspect LABEL ENV prints what the environment's state dir holds after a run.
inspect() {
  local label=$1 d=${RUN_DIR:-$SRC}/.devenv/$2
  if [ ! -d "$d" ]; then obs "[$label] no state dir $d"; return; fi
  obs "[$label] state dir holds: $(ls -A "$d" | tr '\n' ' ')"
  [ -f "$d/dagger.log" ] && obs "[$label] dagger.log lines=$(wc -l <"$d/dagger.log") bytes=$(wc -c <"$d/dagger.log")"
  [ -f "$d/dagger.log" ] && { echo "----- dagger.log tail"; tail -15 "$d/dagger.log"; echo "-----"; }
  local logs=$d/logs
  if [ ! -d "$logs" ]; then obs "[$label] no logs dir"; return; fi
  obs "[$label] logs: $(find "$logs" -type f | wc -l) files, $(du -sh "$logs" | cut -f1); $(find "$logs" -type f -newer "$RUNNER_TEMP/stamp-$label" | wc -l) written by this run"
  obs "[$label] logs top level: $(ls -A "$logs" | tr '\n' ' ')"
  echo "----- logs tree (depth 2)"; find "$logs" -maxdepth 2 | sed "s|$logs/||" | sort | head -40; echo "-----"
  local res=$logs/resources.yaml
  if [ -f "$res" ]; then
    obs "[$label] resources.yaml kinds: $(yq -N '.items[].kind' "$res" 2>/dev/null | sort | uniq -c | tr -s ' ' | tr '\n' ',')"
    echo "----- PackageInstall and App status"
    yq -N '.items[] | select(.kind == "PackageInstall" or .kind == "App") | .kind + " " + .metadata.namespace + "/" + .metadata.name + ": " + (.status.friendlyDescription // "-") + " | " + (.status.usefulErrorMessage // "" | sub("\n"; " "))' "$res" 2>&1 | cut -c1-600
    echo "----- App deploy stdout tails (non-succeeded)"
    yq -N '.items[] | select(.kind == "App" and .status.friendlyDescription != "Reconcile succeeded") | "## " + .metadata.name + "\n" + (.status.deploy.stdout // "(no deploy stdout)")' "$res" 2>&1 | tail -80
    echo "-----"
  else
    obs "[$label] no resources.yaml"
  fi
}

# grep_logs LABEL ENV PATTERN DESCRIPTION says which exported files match.
grep_logs() {
  local label=$1 d=${RUN_DIR:-$SRC}/.devenv/$2/logs pattern=$3 what=$4
  local files
  files=$(grep -rlE "$pattern" "$d" 2>/dev/null | sed "s|$d/||" | head -5 | tr '\n' ' ')
  obs "[$label] $what: ${files:-NOT FOUND in logs}"
}
