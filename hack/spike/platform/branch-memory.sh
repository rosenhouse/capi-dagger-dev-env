#!/usr/bin/env bash
# Compares a --freeze-source branch child with its source VM, without Kubernetes.
set -euo pipefail
cd "$(dirname "$0")"
METRICS=$RUNNER_TEMP/branch-memory.md
. ./lib.sh
trap summary EXIT

measure() { # name command...: records the command's output, and fails if the command fails
	local value
	value=$("${@:2}")
	metric "$1" "$value"
}

timed_in() { # machine shell-command: the command's duration by the guest's clock, then by the host's
	local t0 guest
	t0=$(now)
	guest=$(smolvm machine exec --name "$1" -- sh -c "a=\$(cut -d' ' -f1 /proc/uptime) && $2 && b=\$(cut -d' ' -f1 /proc/uptime) && echo \$a \$b")
	awk -v g="$guest" -v t0="$t0" -v t1="$(now)" 'BEGIN { split(g, u, " "); printf "%.2f s (host %.2f s)", u[2] - u[1], t1 - t0 }'
}

read_cache() { timed_in "$1" 'cat /root/f >/dev/null'; }
# Four writers, one per vCPU, overwrite 2 GiB of tmpfs in place.
rewrite_shm() {
	timed_in "$1" 'p=""; for i in 1 2 3 4; do dd if=/dev/zero of=/dev/shm/f$i bs=1M count=512 conv=notrunc 2>/dev/null & p="$p $!"; done; for x in $p; do wait $x || exit 1; done'
}
busy_loop() { timed_in "$1" 'i=0; while [ $i -lt 1000000 ]; do i=$((i + 1)); done'; }
clock_read() { smolvm machine exec --name "$1" -- python3 /root/clock.py; }
sync_write() { smolvm machine exec --name "$1" -- python3 /root/sync-write.py /storage/wal; }
clocksource() { smolvm machine exec --name "$1" -- cat /sys/devices/system/clocksource/clocksource0/current_clocksource; }

idle_cpu() { # machine: VMM CPU use over 30 s while the guest runs nothing
	local a b
	a=$(vmm_stat "$1")
	sleep 30
	b=$(vmm_stat "$1")
	echo "$a $b" | awk '{ printf "%.2f cores", ($6 - $3) / 30 }'
}

metric "runner CPU" "$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs)"
metric "host THP shmem_enabled" "$(cat /sys/kernel/mm/transparent_hugepage/shmem_enabled)"
smolvm machine create --name mem --net --net-backend virtio-net --cpus 4 --mem 8192
smolvm machine start --name mem --branchable
smolvm machine cp clock.py mem:/root/clock.py
smolvm machine cp sync-write.py mem:/root/sync-write.py
smolvm machine exec --name mem -- sh -c 'for i in 1 2 3 4 5; do apk add -q python3 && exit 0; sleep 5; done; exit 1'
smolvm machine exec --name mem -- sh -euc '
	dd if=/dev/urandom of=/root/f bs=1M count=2048 2>/dev/null
	cat /root/f >/dev/null
	dd if=/dev/zero of=/storage/wal bs=1M count=64 conv=fsync 2>/dev/null
	for i in 1 2 3 4; do dd if=/dev/zero of=/dev/shm/f$i bs=1M count=512 2>/dev/null; done
	free -m'

measure "source: guest clocksource" clocksource mem
measure "source: read 2 GiB of cached file" read_cache mem
measure "source: rewrite 2 GiB of tmpfs" rewrite_shm mem
measure "source: busy loop" busy_loop mem
measure "source: one monotonic clock read in Python" clock_read mem
measure "source: VMM CPU while idle" idle_cpu mem
measure "source: O_DSYNC 4 KiB rewrite, one per 64 KiB of a 64 MiB file" sync_write mem
measure "source: guest clock minus host clock" clock_offset mem

smolvm machine exec --name mem -- /bin/sync
smolvm machine branch --from mem --name child --freeze-source

measure "child: guest clock minus host clock" clock_offset child
measure "child: first read of the cached file" read_cache child
measure "child: second read" read_cache child
measure "child: first rewrite of the tmpfs" rewrite_shm child
measure "child: second rewrite" rewrite_shm child
measure "child: busy loop" busy_loop child
measure "child: one monotonic clock read in Python" clock_read child
measure "child: VMM CPU while idle" idle_cpu child
measure "child: O_DSYNC 4 KiB rewrite, first pass" sync_write child
measure "child: O_DSYNC 4 KiB rewrite, second pass" sync_write child
measure "child: guest clocksource" clocksource child
