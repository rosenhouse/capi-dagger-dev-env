# Helpers for run.sh and branch-memory.sh, which set METRICS before sourcing this.

metric() { echo "metric: $1 = $2"; echo "| $1 | $2 |" >>"$METRICS"; }
now() { date +%s.%N; }
elapsed() { awk -v a="$1" -v b="$(now)" 'BEGIN { printf "%.1f", b - a }'; }
since() { echo "$(elapsed "$1") s"; }

retry() { # seconds command...
	local end=$((SECONDS + $1))
	shift
	until "$@"; do
		((SECONDS < end)) || return 1
		sleep 2
	done
}

vmm_pid() { smolvm machine status --name "$1" --json | jq -r .pid; }

# vmm_stat <machine> prints the VMM's minor faults, major faults and CPU seconds.
# A branch child's VMM is not dumpable, so reading it needs root.
# The process name has a space, so fields count from after its closing parenthesis.
vmm_stat() {
	sudo cat "/proc/$(vmm_pid "$1")/stat" | sed 's/.*) //' | awk '{ printf "%d %d %.1f\n", $8, $10, ($12 + $13) / 100 }'
}

# clock_offset <machine> prints the guest's CLOCK_REALTIME minus the host's, ± half the exec round trip.
# The guest's busybox has no date +%N. adjtimex reports nanoseconds when status has STA_NANO (0x2000).
clock_offset() {
	local t0 guest
	t0=$(now)
	guest=$(smolvm machine exec --name "$1" -- sh -c "adjtimex | awk '/status/ { st = \$2 } /tv_sec/ { s = \$2 } /tv_usec/ { u = \$2 }
		END { if (int(st / 8192) % 2) printf \"%d.%09d\", s, u; else printf \"%d.%06d\", s, u }'")
	awk -v g="$guest" -v t0="$t0" -v t1="$(now)" 'BEGIN { printf "%.2f s ± %.2f s", g - (t0 + t1) / 2, (t1 - t0) / 2 }'
}

summary() {
	{
		echo "| measurement | value |"
		echo "| --- | --- |"
		cat "$METRICS"
	} | tee -a "$GITHUB_STEP_SUMMARY"
}
