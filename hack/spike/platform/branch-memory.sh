#!/usr/bin/env bash
# Compares a source VM with its --freeze-source branch, without Kubernetes:
# reads of 2 GiB of guest page cache, a CPU-only loop, clock reads, and the VMM's CPU use while idle.
set -euo pipefail
cd "$(dirname "$0")"
OUT=$RUNNER_TEMP/branch-memory
mkdir -p "$OUT"

metric() { echo "metric: $1 = $2"; echo "| $1 | $2 |" >>"$OUT/metrics.md"; }

timed_in() { # machine shell-command: prints the guest's own timing of the command
	smolvm machine exec --name "$1" -- sh -c "a=\$(cut -d' ' -f1 /proc/uptime); $2; b=\$(cut -d' ' -f1 /proc/uptime); echo \$a \$b" |
		awk '{ printf "%.2f s", $2 - $1 }'
}

read_cache() { timed_in "$1" 'cat /root/f >/dev/null'; }
busy_loop() { timed_in "$1" 'i=0; while [ $i -lt 1000000 ]; do i=$((i + 1)); done'; }
clock_read() { smolvm machine exec --name "$1" -- python3 /root/clock.py; }

vmm_cpu() { # machine: CPU seconds of its VMM process
	local pid
	pid=$(smolvm machine status --name "$1" --json | jq -r .pid)
	sudo cat "/proc/$pid/stat" | sed 's/.*) //' | awk '{ printf "%.1f\n", ($12 + $13) / 100 }'
}

idle_cpu() { # machine: VMM CPU use over 30 s while the guest runs nothing
	local a b
	a=$(vmm_cpu "$1")
	sleep 30
	b=$(vmm_cpu "$1")
	awk -v a="$a" -v b="$b" 'BEGIN { printf "%.2f cores", (b - a) / 30 }'
}

metric "runner CPU" "$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs)"
metric "host THP shmem_enabled" "$(cat /sys/kernel/mm/transparent_hugepage/shmem_enabled)"
smolvm machine create --name mem --net --net-backend virtio-net --cpus 4 --mem 8192
smolvm machine start --name mem --branchable
smolvm machine cp clock.py mem:/root/clock.py
smolvm machine exec --name mem -- apk add -q python3
smolvm machine exec --name mem -- sh -c 'dd if=/dev/urandom of=/root/f bs=1M count=2048 2>/dev/null && sync && cat /root/f >/dev/null && free -m'
metric "guest clocksource" "$(smolvm machine exec --name mem -- cat /sys/devices/system/clocksource/clocksource0/current_clocksource)"
metric "source: read 2 GiB of cached file" "$(read_cache mem)"
metric "source: busy loop" "$(busy_loop mem)"
metric "source: one monotonic clock read in Python" "$(clock_read mem)"
metric "source: VMM CPU while idle" "$(idle_cpu mem)"
smolvm machine exec --name mem -- /bin/sync
smolvm machine branch --from mem --name child --freeze-source
metric "child: first read of the cached file" "$(read_cache child)"
metric "child: second read" "$(read_cache child)"
metric "child: busy loop" "$(busy_loop child)"
metric "child: one monotonic clock read in Python" "$(clock_read child)"
metric "child: VMM CPU while idle" "$(idle_cpu child)"
metric "child: guest clocksource" "$(smolvm machine exec --name child -- cat /sys/devices/system/clocksource/clocksource0/current_clocksource)"
smolvm machine exec --name child -- sh -c 'dmesg | tail -20'
