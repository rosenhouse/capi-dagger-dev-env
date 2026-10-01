# Helpers for exploring warm starts in CI. Notes go to $OUT/notes.md and the job summary.
set -uo pipefail
D=$RUNNER_TEMP/devenv
OUT=$RUNNER_TEMP/explore
CACHE=$HOME/.cache/devenv/platform
mkdir -p "$OUT"
go build -o "$D" ./cmd/devenv

say() { echo "$*" | tee -a "$OUT/notes.md"; }
note() { say ""; say "### $*"; }
secs() { awk "BEGIN { printf \"%.1f\", $EPOCHREALTIME - $1 }"; }
quote() { sed 's/^/    /' | tee -a "$OUT/notes.md"; }

# timed NAME CMD... runs CMD with its output in $OUT/NAME.log, and notes its exit status, time and key lines.
timed() {
	local name=$1 start=$EPOCHREALTIME status=0
	shift
	"$@" >"$OUT/$name.log" 2>&1 || status=$?
	say "- $name: exit $status after $(secs "$start") s"
	grep -vE '^\[ *[0-9.]+s\] [^:]+$' "$OUT/$name.log" | tail -n 25 | quote
	return 0
}

# interrupt NAME PATTERN DELAY CMD... runs CMD in its own process group, as a terminal would,
# and sends the group SIGINT DELAY seconds after PATTERN appears in its output.
interrupt() {
	local name=$1 pattern=$2 delay=$3 log=$OUT/$1.log start=$EPOCHREALTIME
	shift 3
	setsid "$@" >"$log" 2>&1 &
	local pid=$!
	until grep -qE "$pattern" "$log"; do
		if ! kill -0 "$pid" 2>/dev/null; then
			say "- $name: exited before /$pattern/"
			break
		fi
		sleep 0.2
	done
	sleep "$delay"
	local at
	at=$(secs "$start")
	kill -INT -- "-$pid" 2>/dev/null
	local status=0
	wait "$pid" || status=$?
	say "- $name: SIGINT at $at s; exit $status after $(secs "$start") s"
	tail -n 12 "$log" | quote
}

# Records the data dir of every machine every 2 s, to look for leftovers after a VM is deleted.
watch_data_dirs() {
	while sleep 2; do
		for m in $(smolvm machine ls -q 2>/dev/null); do
			smolvm machine data-dir --name "$m" 2>/dev/null
		done
	done >>"$OUT/data-dirs" &
}

leftovers() {
	say "- machines: [$(smolvm machine ls -q | tr '\n' ' ')]"
	say "- smolvm processes: $(pgrep -af '_boot-vm|smolvm' | grep -cv pgrep)"
	say "- disk used: $(df --output=used -BM / | tail -1 | tr -d ' ')"
	say "- platform cache: $(find "$CACHE" -maxdepth 1 -type f -printf '%f %s; ' 2>/dev/null)"
	if [ -f "$OUT/data-dirs" ]; then
		for d in $(sort -u "$OUT/data-dirs"); do
			if [ -e "$d" ]; then say "- left data dir $d: $(du -sh "$d" | cut -f1)"; fi
		done
	fi
}

cpu() {
	note "Runner"
	say "- CPU: $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs)"
	say "- contract: $("$D" platform key --inputs | jq -r .contract)"
	say "- key: $("$D" platform key)"
}

summary() { cat "$OUT/notes.md" >>"$GITHUB_STEP_SUMMARY"; }
trap summary EXIT
