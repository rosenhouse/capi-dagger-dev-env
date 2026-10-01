#!/usr/bin/env bash
# Runs as root until killed: sampler-host.sh <out-dir>.
# Every 5 s it appends host memory, /proc/vmstat, each VMM in <out-dir>/pids.log with its KVM
# statistics, and cumulative KVM tracepoint histograms to <out-dir>/host-samples.log.
# report.py reads the result.
set -u
out=$1
umask 022
mountpoint -q /sys/kernel/debug || mount -t debugfs none /sys/kernel/debug
tracing=/sys/kernel/tracing
mountpoint -q $tracing || mount -t tracefs none $tracing

# event;histogram keys and values. madvise shows the balloon handing guest pages back with MADV_DONTNEED.
EVENTS="kvm/kvm_exit;keys=exit_reason:vals=hitcount
	kvm/kvm_page_fault;keys=error_code:vals=hitcount
	kvmmmu/kvm_mmu_spte_requested;keys=level:vals=hitcount
	kvm/kvm_unmap_hva_range;keys=common_pid.execname:vals=hitcount
	syscalls/sys_enter_madvise;keys=common_pid.execname,behavior:vals=hitcount,len_in"
for e in $EVENTS; do
	echo "hist:${e#*;}:size=4096" >>"$tracing/events/${e%%;*}/trigger" || echo "no histogram for ${e%%;*}"
done

while :; do
	echo "T $(date +%s.%N)"
	echo "M $(awk '{ printf "%s%s ", $1, $2 }' /proc/meminfo)"
	echo "V $(tr ' \n' '= ' </proc/vmstat)"
	for pid in $(awk '{ print $3 }' "$out/pids.log" 2>/dev/null | sort -u); do
		[ -r "/proc/$pid/stat" ] || continue
		echo "P $pid $(sed 's/.*) //' "/proc/$pid/stat") $(awk '/^(RssAnon|RssFile|RssShmem|Threads):/ { printf "%s%s ", $1, $2 }' "/proc/$pid/status")"
		for d in /sys/kernel/debug/kvm/"$pid"-*; do
			[ -d "$d" ] && echo "K $pid $(cd "$d" && grep -s . -- * | tr ':\n' '= ')"
		done
	done
	for e in $EVENTS; do
		grep -s '^{' "$tracing/events/${e%%;*}/hist" | sed "s|^|H ${e%%;*} |"
	done
	sleep 5
done >>"$out/host-samples.log" 2>>"$out/sampler-host.err"
