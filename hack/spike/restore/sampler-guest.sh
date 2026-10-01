#!/bin/sh
# Runs in the guest until the VM goes away: sh sampler-guest.sh.
# Every 5 s it appends CPU, memory and interrupt counters, /readyz of both API servers and the
# busiest processes to /dev/shm/m1.log. The log lives in guest RAM, so checkpoints, restores and
# branches carry it and the sampler on. report.py reads it.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
log=/dev/shm/m1.log
ticks=/dev/shm/m1.ticks

cpu_ticks() { # pid utime+stime comm, for every process including kernel threads
	cat /proc/[0-9]*/stat 2>/dev/null | sed 's/^\([0-9]*\) (\(.*\)) /\1 \2 /' |
		awk '{ n = NF; print $1, $(n - 38) + $(n - 37), $2 }'
}

readyz() { curl -sk -o /dev/null -w '%{http_code}' --max-time 2 "https://127.0.0.1:$1/readyz"; }

docker ps --format 'D {{.ID}} {{.Names}}' >>$log
cpu_ticks >$ticks
while :; do
	{
		echo "T $(cut -d' ' -f1 /proc/uptime) $(date +%s) $(cut -d' ' -f1-4 /proc/loadavg)"
		echo "S $(head -1 /proc/stat) $(awk '$1 ~ /^(ctxt|intr|procs_running|procs_blocked)$/ { printf "%s %s ", $1, $2 }' /proc/stat)"
		echo "V $(awk '$1 ~ /^(pgfault|pgmajfault|pgfree|pgalloc_normal|thp_fault_alloc|thp_split_pmd|thp_collapse_alloc|compact_stall|pgscan_kswapd|pgscan_direct|pgsteal_kswapd|nr_free_pages|workingset_refault_file|pgpgin|pgpgout|oom_kill|pgmigrate_success)$/ { printf "%s=%s ", $1, $2 }' /proc/vmstat)"
		echo "I $(awk '$1 ~ /^[0-9]+:$/ { for (i = 2; i <= NF && $i ~ /^[0-9]+$/; i++) d += $i; next }
			$1 ~ /^[A-Z]+:$/ { s = 0; for (i = 2; i <= NF && $i ~ /^[0-9]+$/; i++) s += $i; printf "%s%d ", $1, s }
			END { printf "DEV:%d", d }' /proc/interrupts)"
		echo "R $(readyz 6443) $(readyz 7443)"
		cpu_ticks >$ticks.new
		echo "Q $(awk 'NR == FNR { a[$1] = $2; next } ($1 in a) && $2 > a[$1] { print $2 - a[$1], $1, $3 }' $ticks $ticks.new |
			sort -rn | head -8 | while read -r t pid comm; do
				id=$(sed -n 's|^0::/docker/\([0-9a-f]\{12\}\).*|\1|p' "/proc/$pid/cgroup" 2>/dev/null)
				printf '%s:%s@%s ' "$t" "$comm" "${id:-vm}"
			done)"
		mv $ticks.new $ticks
	} >>$log 2>/dev/null
	sleep 5
done
