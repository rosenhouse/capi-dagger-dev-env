#!/usr/bin/env bash
# E1: checkpoint, restore and branch mechanics on a tiny bare VM.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
ckpt=$RUNNER_TEMP/tiny.checkpoint

# Branch VMMs are not dumpable, so their /proc files need root.
rss() { local pid; pid=$(smolvm machine status --name "$1" --json | jq -r .pid); sudo grep -E '^(Rss|Pss_Shmem):' "/proc/$pid/smaps_rollup" | tr -s ' \n' ' '; }
state() { smolvm machine status --name "$1" --json | jq -r .state; }
outcome() { local out rc=0; out=$("$@" 2>&1) || rc=$?; echo "exit $rc: $(tr '\n' ' ' <<<"$out" | cut -c1-300)"; }
listening() { ss -ltnH | awk '{ print $4 }' | grep -oE ':(1808[0-9]|28080)$' | sort | tr '\n' ' '; }

summary "## E1 on $(cpu_model), $(host_info)"
summary "| measurement | value |"
summary "|---|---|"

echo "::group::C1 capture, delete, restore"
boot_tiny tiny 18080
wait_serving 18080
sleep 20
read -r h0 g0 u0 < <(clock tiny)
c0=$(probe 18080)
measure "tiny: served before checkpoint (pid count)" "$c0"
checkpoint tiny "$ckpt"
measure "contract in checkpoint manifest" "$(file_contract "$ckpt")"
measure "contract from lib.sh host_contract" "$(host_contract)"
manifest "$ckpt" | jq -c '.checkpoint | {cpus, memory_mib, storage_gib, overlay_gib, memory, disks, network}'
smolvm machine delete --name tiny -f
sleep 30 # makes the clock jump stand out
timed "tiny: create --from (capturing VM deleted)" smolvm machine create --name tiny --from "$ckpt"
timed "tiny: start (restore)" smolvm machine start --name tiny
c1=$(probe 18080)
read -r h1 g1 u1 < <(clock tiny)
measure "tiny: served after restore (pid count)" "$c1"
measure "host time from pre-capture clock read to post-restore read" "$(diff_s "$h0" "$h1")"
measure "guest uptime advance over the same span" "$(diff_s "$u0" "$u1")"
measure "guest realtime minus host realtime, before capture" "$(diff_s "$h0" "$g0")"
measure "guest realtime minus host realtime, after restore" "$(diff_s "$h1" "$g1")"
measure "tiny: VMM after restore" "$(rss tiny)"
measure "tiny: guest clocksource" "$(guest tiny 'cat /sys/devices/system/clocksource/clocksource0/current_clocksource')"
guest tiny 'dmesg | tail -15'
sleep 5
read -r h2 _ u2 < <(clock tiny)
measure "tiny: guest/host clock rate after restore" "$(rate "$h1" "$u1" "$h2" "$u2")"
echo "::endgroup::"

echo "::group::C2 two branches with pinned ports"
timed "tiny-a: branch --freeze-source -p 18081:8080" smolvm machine branch --from tiny --name tiny-a --freeze-source -p 18081:8080
timed "tiny-b: branch -p 18082:8080" smolvm machine branch --from tiny --name tiny-b -p 18082:8080
a1=$(probe 18081) b1=$(probe 18082)
sleep 3
measure "tiny-a served, 3 s apart" "$a1 → $(probe 18081)"
measure "tiny-b served, 3 s apart" "$b1 → $(probe 18082)"
guest tiny-a 'echo from-a >/dev/shm/mark'
measure "tiny-b sees tiny-a's /dev/shm/mark" "$(guest tiny-b 'cat /dev/shm/mark 2>/dev/null || echo no')"
read -r h g u < <(clock tiny-a)
measure "tiny-a: guest realtime minus host, uptime" "$(diff_s "$h" "$g"), uptime $u s"
measure "source state with two branches" "$(state tiny)"
measure "exec in frozen source" "$(outcome guest tiny true)"
t=$EPOCHREALTIME
measure "probe frozen source's port 18080" "'$(probe 18080)' after $(since "$t") s"
for m in tiny tiny-a tiny-b; do measure "$m: VMM" "$(rss "$m")"; done
measure "host ports listening" "$(listening)"
echo "::endgroup::"

echo "::group::C3 rebind a restored machine's host port; C6 restore one checkpoint twice"
smolvm machine create --name tiny-dup --from "$ckpt"
measure "tiny-dup: start on 18080 while frozen tiny holds it" "$(outcome smolvm machine start --name tiny-dup)"
measure "tiny-dup: state after that start" "$(state tiny-dup)"
smolvm machine delete --name tiny-dup -f || true
smolvm machine create --name tiny-u --from "$ckpt"
measure "tiny-u: update --remove-port 18080:8080 -p 28080:8080 while Created" \
  "$(outcome smolvm machine update --name tiny-u --remove-port 18080:8080 -p 28080:8080)"
smolvm machine ls --verbose
timed "tiny-u: start (restore)" smolvm machine start --name tiny-u
measure "tiny-u: served on 28080" "$(probe 28080)"
measure "tiny-u: hostname" "$(guest tiny-u hostname)"
measure "host ports listening" "$(listening)"
echo "::endgroup::"

echo "::group::C4 stop/start, sibling deletion, cascade"
timed "tiny-b: stop" smolvm machine stop --name tiny-b
measure "tiny-b: start after stop" "$(outcome smolvm machine start --name tiny-b)"
measure "tiny-b: served after stop/start" "'$(probe 18082)'"
measure "tiny-b: uptime and counter after stop/start" "$(outcome guest tiny-b 'cut -d" " -f1 /proc/uptime; cat /dev/shm/n')"
timed "tiny-u: stop" smolvm machine stop --name tiny-u
measure "tiny-u: start after stop" "$(outcome smolvm machine start --name tiny-u)"
measure "tiny-u: served after stop/start" "'$(probe 28080)'"
measure "tiny-u: uptime and counter after stop/start" "$(outcome guest tiny-u 'cut -d" " -f1 /proc/uptime; cat /dev/shm/n')"
timed "tiny-b: delete -f" smolvm machine delete --name tiny-b -f
measure "tiny-a served after tiny-b deleted" "$(probe 18081)"
measure "tiny-c: branch without -p" "$(outcome smolvm machine branch --from tiny --name tiny-c)"
dirs=$(for m in tiny tiny-a tiny-c; do smolvm machine data-dir --name "$m"; done)
measure "source state before cascade" "$(state tiny)"
timed "delete tiny --cascade" smolvm machine delete --name tiny --cascade
measure "machines left" "$(smolvm machine ls -q | tr '\n' ' ')"
measure "data dirs left of tiny, tiny-a, tiny-c" "$(for d in $dirs; do [ -e "$d" ] && echo "$d"; done | tr '\n' ' ')"
measure "VMM processes left" "$(pgrep -fc _boot-vm || true)"
measure "host ports listening" "$(listening)"
echo "::endgroup::"

echo "::group::C6 restore again after every machine from it is gone"
smolvm machine delete --name tiny-u -f
timed "tiny-z: create --from (second time on this host)" smolvm machine create --name tiny-z --from "$ckpt"
timed "tiny-z: start (restore)" smolvm machine start --name tiny-z
measure "tiny-z: served" "$(probe 18080)"
smolvm machine delete --name tiny-z -f
echo "::endgroup::"
