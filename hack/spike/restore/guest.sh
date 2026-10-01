#!/bin/sh
# Runs as root in the guest: sh /root/restore-guest.sh <stage> [label].
# Lines starting with "metric|" are measurements that run.sh records.
set -eu
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=/root

metric() { echo "metric|$1|$2"; }

# The capture syncs again, and that sync must finish within 30 s.
pre_capture() {
	sync
	fstrim -v /storage
}

# Counters that show churn after a restore or branch: container restarts, leader changes,
# and nodes going NotReady.
churn() { # label
	for kc in /root/.kube/config /root/work.kubeconfig; do
		c=mgmt
		[ $kc = /root/.kube/config ] || c=work
		metric "$c: container restarts, lease transitions, NodeNotReady events, LeaderElection events ($1)" \
			"$(restarts $kc), $(lease_transitions $kc), $(events $kc NodeNotReady), $(events $kc LeaderElection)"
		metric "$c: restarted containers ($1)" "$(kubectl --kubeconfig $kc get pods -A -o jsonpath='{range .items[*]}{range .status.containerStatuses[?(@.restartCount>0)]}{.name}={.restartCount} {end}{end}')"
	done
}

restarts() { # kubeconfig
	kubectl --kubeconfig "$1" get pods -A -o jsonpath='{range .items[*]}{range .status.containerStatuses[*]}{.restartCount}{"\n"}{end}{end}' |
		awk '{ s += $1 } END { print s + 0 }'
}

lease_transitions() { # kubeconfig
	kubectl --kubeconfig "$1" get leases -A -o jsonpath='{range .items[*]}{.spec.leaseTransitions}{"\n"}{end}' |
		awk '{ s += $1 } END { print s + 0 }'
}

events() { # kubeconfig reason
	kubectl --kubeconfig "$1" get events -A --field-selector reason="$2" --no-headers 2>/dev/null | wc -l
}

oom_lines() {
	n=$(dmesg | grep -ciE 'out of memory|oom-kill|killed process' || true)
	metric "guest kernel OOM lines ($(hostname))" "$n"
	[ "$n" = 0 ]
}

case $1 in
pre-capture) pre_capture ;;
churn) churn "$2" ;;
oom-lines) oom_lines ;;
*) echo "unknown stage $1" >&2; exit 2 ;;
esac
