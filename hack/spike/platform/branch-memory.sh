#!/usr/bin/env bash
# Times reads of 2 GiB of guest page cache in a source VM, then twice in its --freeze-source branch,
# and a CPU-only loop in each, to separate first-touch memory cost from CPU speed.
set -euo pipefail
OUT=$RUNNER_TEMP/branch-memory
mkdir -p "$OUT"

metric() { echo "metric: $1 = $2"; echo "| $1 | $2 |" >>"$OUT/metrics.md"; }

timed_in() { # machine shell-command: prints the guest's own timing of the command
	smolvm machine exec --name "$1" -- sh -c "a=\$(cut -d' ' -f1 /proc/uptime); $2; b=\$(cut -d' ' -f1 /proc/uptime); echo \$a \$b" |
		awk '{ printf "%.2f s", $2 - $1 }'
}

read_cache() { timed_in "$1" 'cat /root/f >/dev/null'; }
busy_loop() { timed_in "$1" 'i=0; while [ $i -lt 1000000 ]; do i=$((i + 1)); done'; }

metric "host THP shmem_enabled" "$(cat /sys/kernel/mm/transparent_hugepage/shmem_enabled)"
smolvm machine create --name mem --net --net-backend virtio-net --cpus 4 --mem 8192
smolvm machine start --name mem --branchable
smolvm machine exec --name mem -- sh -c 'dd if=/dev/urandom of=/root/f bs=1M count=2048 2>/dev/null && sync && cat /root/f >/dev/null && free -m'
metric "source: read 2 GiB of cached file" "$(read_cache mem)"
metric "source: busy loop" "$(busy_loop mem)"
smolvm machine exec --name mem -- /bin/sync
smolvm machine branch --from mem --name child --freeze-source
metric "child: first read of the cached file" "$(read_cache child)"
metric "child: second read" "$(read_cache child)"
metric "child: busy loop" "$(busy_loop child)"
pid=$(smolvm machine status --name child --json | jq -r .pid)
metric "child VMM minor faults" "$(sudo cat "/proc/$pid/stat" | sed 's/.*) //' | awk '{ print $8 }')"
