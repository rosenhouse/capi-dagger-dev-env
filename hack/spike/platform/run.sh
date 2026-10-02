#!/usr/bin/env bash
# Host side of the platform spike: run.sh <stage>. Stages run in workflow order.
# PLAT is brought up from scratch, checkpointed twice and deleted. SRC is restored from one of the
# checkpoints and branched into environments that publish host ports.
set -euo pipefail
cd "$(dirname "$0")"

PLAT=plat
SRC=src
GUEST_REGISTRY=172.31.255.254:5000
BUSYBOX=busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
OUT=$RUNNER_TEMP/platform
CK=$RUNNER_TEMP/checkpoints
METRICS=$OUT/metrics.md
mkdir -p "$OUT" "$CK"
. ./lib.sh

ports() { # env: host ports of its mgmt API, work API and session registry
	case $1 in
	env-a) echo 26443 27443 25000 ;;
	env-b) echo 28443 29443 25001 ;;
	*) return 1 ;;
	esac
}

timed() { # name command...
	local name=$1 t0
	shift
	t0=$(now)
	"$@"
	metric "$name" "$(since "$t0")"
}

# timed_to <file> <name> <command...>: like timed, and also writes the seconds to $OUT/<file>.
timed_to() {
	local file=$1 name=$2 t0
	shift 2
	t0=$(now)
	"$@"
	elapsed "$t0" >"$OUT/$file"
	metric "$name" "$(cat "$OUT/$file") s"
}

# guest <machine> <stage> [arg]: runs a guest.sh stage and records its metric lines.
guest() {
	local log="$OUT/guest-$1-$2${3:+-${3//[^a-zA-Z0-9]/-}}.log" rc=0
	smolvm machine exec --name "$1" --stream --timeout 30m -- sh /root/guest.sh "${@:2}" | tee "$log" || rc=$?
	sed -n 's/^metric|\(.*\)|\(.*\)$/| \1 | \2 |/p' "$log" >>"$METRICS"
	return "$rc"
}

state() { smolvm machine status --name "$1" --json | jq -r .state; }
uptime_of() { smolvm machine exec --name "$1" -- cut -d' ' -f1 /proc/uptime; }
host_mem() { free -m | awk '/^Mem:/ { m = "used=" $3 " avail=" $7 } /^Swap:/ { s = " swap=" $3 } END { print m s }'; }

host_tools() {
	local bin=$OUT/bin
	mkdir -p "$bin"
	curl -fsSL --retry 5 --retry-all-errors -o "$bin/kubectl" https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl
	echo "6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f  $bin/kubectl" | sha256sum -c -
	chmod +x "$bin/kubectl"
	curl -fsSL --retry 5 --retry-all-errors -o "$OUT/ggcr.tgz" https://github.com/google/go-containerregistry/releases/download/v0.22.1/go-containerregistry_Linux_x86_64.tar.gz
	echo "0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0  $OUT/ggcr.tgz" | sha256sum -c -
	tar -xzf "$OUT/ggcr.tgz" -C "$bin" crane
	echo "$bin" >>"$GITHUB_PATH"
}

boot() {
	metric "runner CPU" "$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs)"
	metric "smolvm" "$(smolvm --version)"
	metric "host THP shmem_enabled" "$(cat /sys/kernel/mm/transparent_hugepage/shmem_enabled)"
	metric "host memory total, swap total" "$(free -m | awk '/^Mem:/ { m = $2 } /^Swap:/ { s = $2 } END { print m " MiB, " s " MiB" }')"
	metric "host systemd-oomd" "$(systemctl is-active systemd-oomd || true)"
	now >"$OUT/bringup-at"
	smolvm machine create --name $PLAT --net --net-backend virtio-net --cpus 4 --mem 12288 --storage 40 --overlay 10
	timed "first start of the bare VM (--branchable)" smolvm machine start --name $PLAT --branchable
	for f in guest.sh kind-mgmt.yaml kindnet-crs.yaml ../../../internal/devenv/platform/kindnet.yaml; do
		smolvm machine cp "$f" "$PLAT:/root/$(basename "$f")"
	done
	timed "guest install (apk, Docker, kind, kubectl, clusterctl, manifests)" guest $PLAT install
}

docker_up() {
	guest $PLAT setup
	smolvm machine exec --name $PLAT -d -- sh /root/guest.sh dockerd
	timed "dockerd ready" guest $PLAT docker-facts
	guest $PLAT kernel-probe
}

mgmt() {
	timed "kind network and session registry" guest $PLAT registry
	timed "docker pull kindest/node" guest $PLAT pull
	timed "kind create cluster mgmt, node Ready" guest $PLAT kind
}

platform() {
	timed "clusterctl init (cert-manager, CAPI, kubeadm, CAPD)" guest $PLAT capi
	timed "kapp-controller rolled out" guest $PLAT kapp
	timed "workload manifests applied" guest $PLAT workload
	timed "Cluster work Available, 2 nodes Ready" guest $PLAT workload-wait
	timed "work API forwarder answers on 7443" guest $PLAT forward
	metric "full bring-up: machine create to work API on 7443" "$(since "$(cat "$OUT/bringup-at")")"
	smolvm machine exec --name $PLAT -- cat /root/.kube/config >"$OUT/mgmt.kubeconfig"
	smolvm machine exec --name $PLAT -- cat /root/work.kubeconfig >"$OUT/work.kubeconfig"
}

# resources <label> <machine>: host memory, every VMM's memory and disk, and the machine's guest
# memory, disk, CPU and clock.
resources() {
	local m
	free -m
	metric "host memory, MiB ($1)" "$(host_mem)"
	host_meminfo "$1"
	for m in $(smolvm machine ls -q); do
		vmm_memory "$m" "$1"
		metric "host disk of $m ($1)" "$(du -sm "$(smolvm machine data-dir --name "$m")" | cut -f1) MiB"
	done
	guest "$2" resources "$1"
	cpu_probe "$2" "$1"
}

host_meminfo() { # label
	metric "host MemAvailable, Cached, Dirty, AnonPages, Mapped, Shmem ($1)" \
		"$(awk '/^(MemAvailable|Cached|Dirty|AnonPages|Mapped|Shmem):/ { printf "%s%d MiB", sep, $2 / 1024; sep = ", " }' /proc/meminfo)"
}

# A branch child's VMM is not dumpable, so its /proc files need root.
vmm_memory() { # machine label
	local f="$OUT/smaps-$1-${2//[^a-zA-Z0-9]/-}.txt"
	sudo cat "/proc/$(vmm_pid "$1")/smaps_rollup" >"$f"
	metric "VMM $1 Rss, Pss, Pss_Shmem ($2)" "$(awk '/^(Rss|Pss|Pss_Shmem):/ { printf "%s%d MiB", sep, $2 / 1024; sep = ", " }' "$f")"
}

vmm_smaps() { # machine field: one smaps_rollup field of the VMM, in MiB
	sudo awk -v f="$2:" '$1 == f { printf "%d", $2 / 1024 }' "/proc/$(vmm_pid "$1")/smaps_rollup"
}

# cpu_probe <machine> <label>: guest CPU use, and the guest clock against the host's.
cpu_probe() {
	metric "guest clock minus host clock ($1, $2)" "$(clock_offset "$1")"
	guest "$1" cpu-probe "$2"
}

strip_ansi() { sed 's/\x1b\[[0-9;]*m//g' "$@"; }

# capture <name>: checkpoints PLAT to $CK/<name>.checkpoint.
capture() {
	local f=$CK/$1.checkpoint log=$OUT/capture-$1.log used t0
	guest $PLAT pre-capture
	resources "before $1 capture" $PLAT
	used=$(df -m --output=used / | tail -1)
	echo "$(now) $(uptime_of $PLAT)" >"$CK/$1.clock"
	t0=$(now)
	RUST_LOG=smolvm=info smolvm machine checkpoint --name $PLAT -o "$f" 2>&1 | tee "$log"
	metric "$1 checkpoint: capture time" "$(since "$t0")"
	metric "$1 checkpoint: file size" "$(stat -c %s "$f" | awk '{ printf "%d MiB", $1 / 1048576 }')"
	metric "$1 checkpoint: host disk added" "$(($(df -m --output=used / | tail -1) - used)) MiB"
	metric "$1 checkpoint: smolvm summary" "$(strip_ansi "$log" | grep -o "Checkpointed '.*" | sed 's/^[^(]*//')"
	strip_ansi "$log" | sed -nE 's/.*phase="?([a-z_]+)"? elapsed_ms=([0-9]+).*/\1 \2/p' |
		while read -r phase ms; do metric "$1 checkpoint: phase $phase" "$ms ms"; done
	metric "state of $PLAT after capture" "$(state $PLAT)"
	settle $PLAT "after $1 capture"
}

# settle <machine> <label>: waits up to 15 min for the guest to be at least 60% idle over 10 s.
settle() {
	local t0
	t0=$(now)
	if retry 900 idle_at_least 60 "$1"; then
		metric "$1 at least 60% idle ($2)" "$(since "$t0")"
	else
		metric "$1 at least 60% idle ($2)" "not after $(since "$t0")"
	fi
}

idle_at_least() { # percent machine
	local idle
	idle=$(smolvm machine exec --name "$2" -- sh -c 'head -1 /proc/stat; sleep 10; head -1 /proc/stat' | awk '
		NR == 1 { for (i = 2; i <= 9; i++) a[i] = $i; next }
		{ for (i = 2; i <= 9; i++) { d = $i - a[i]; t += d; if (i == 5) idle = d } }
		END { printf "%d", 100 * idle / t }')
	echo "$(date +%T) $2 idle $idle%"
	((idle >= $1))
}

host_avail() { free -m | awk '/^Mem:/ { print $7 }'; }
guest_avail() { smolvm machine exec --name "$1" -- awk '/^MemAvailable:/ { print int($2 / 1024) }' /proc/meminfo; }

# zero_fill drops the guest's page cache, then writes zeros into guest tmpfs in 512 MiB steps,
# so that stale pages become zero pages, which capture skips.
# Each step can cost the host 512 MiB, so it stops while the host still has 4 GiB available:
# an unbounded fill makes the runner shut down.
zero_fill() {
	local i=0 rss0 t0
	host_meminfo "before guest drop_caches"
	smolvm machine exec --name $PLAT -- sh -c 'sync; echo 3 >/proc/sys/vm/drop_caches; mkdir -p /mnt/zero; mount -t tmpfs -o size=100% zero /mnt/zero'
	sleep 10
	host_meminfo "10 s after guest drop_caches"
	vmm_memory $PLAT "10 s after guest drop_caches"
	rss0=$(vmm_smaps $PLAT Rss)
	t0=$(now)
	while (($(host_avail) > 4096 && $(guest_avail $PLAT) > 2048)); do
		smolvm machine exec --name $PLAT -- dd if=/dev/zero of=/mnt/zero/$i bs=1M count=512
		i=$((i + 1))
		echo "zero-filled $((i * 512)) MiB in $(since "$t0"): host available $(host_avail) MiB," \
			"VMM Rss $(vmm_smaps $PLAT Rss) MiB, $(head -1 /proc/pressure/memory)"
	done
	metric "zero-filled guest memory" "$((i * 512)) MiB in $(since "$t0"), VMM Rss grew $(($(vmm_smaps $PLAT Rss) - rss0)) MiB"
	metric "host, guest MemAvailable when the zero fill stopped" "$(host_avail) MiB, $(guest_avail $PLAT) MiB"
	smolvm machine exec --name $PLAT -- sh -c 'rm -f /mnt/zero/*; umount /mnt/zero'
}

zeroed_capture() {
	zero_fill
	settle $PLAT "after zero fill"
	capture zeroed
}

# restore <checkpoint>: replaces PLAT with SRC, restored from $CK/<checkpoint>.checkpoint.
restore() {
	local used h0 u0 h1 u1
	smolvm machine delete --name $PLAT -f
	metric "restored checkpoint" "$1"
	used=$(df -m --output=used / | tail -1)
	timed_to create-s "restore: create --from" smolvm machine create --name $SRC --from "$CK/$1.checkpoint"
	metric "restore: host disk added by create --from" "$(($(df -m --output=used / | tail -1) - used)) MiB"
	metric "restore: largest files under \$HOME (allocated MiB)" \
		"$(find "$HOME" -xdev -type f -size +256M -printf '%b %p\n' 2>/dev/null | sort -rn | head -8 |
			awk -v h="$HOME/" '{ sub(h, "", $2); printf "%s%s %d", sep, $2, $1 / 2048; sep = ", " }')"
	timed_to start-s "restore: start" smolvm machine start --name $SRC
	h1=$(now)
	u1=$(uptime_of $SRC)
	read -r h0 u0 <"$CK/$1.clock"
	metric "restore: host time from the pre-capture clock read" "$(awk -v a="$h0" -v b="$h1" 'BEGIN { printf "%.1f s", b - a }')"
	metric "restore: guest uptime advance over the same span" "$(awk -v a="$u0" -v b="$u1" 'BEGIN { printf "%.1f s", b - a }')"
	metric "restore: guest clock minus host clock" "$(clock_offset $SRC)"
	echo "$u1" >"$OUT/guest-uptime-at-restore"
	cut -d' ' -f1 /proc/uptime >"$OUT/host-uptime-at-restore"
	vmm_memory $SRC "after restore"
	host_meminfo "after restore"
	monitor >"$OUT/monitor.log" 2>&1 &
	echo $! >"$OUT/monitor.pid"
}

# monitor: every 5 s, host memory and memory pressure, and each VMM's CPU seconds and Pss.
monitor() {
	set +e
	local m
	while :; do
		printf '%s %s %s' "$(date +%T)" "$(host_mem)" "$(awk '/^some/ { print "psi-" $2 }' /proc/pressure/memory)"
		for m in $(smolvm machine ls -q); do
			printf ' %s=%s/%sMiB' "$m" "$(vmm_stat "$m" | cut -d' ' -f3)" "$(vmm_smaps "$m" Pss)"
		done
		echo
		sleep 5
	done
}

branch() { # env
	local m w r p freeze=
	read -r m w r < <(ports "$1")
	for p in $m $w $r; do
		if ss -Htln "sport = :$p" | grep -q .; then
			echo "host port $p is in use" >&2
			return 1
		fi
	done
	if [ "$(state $SRC)" = running ]; then
		smolvm machine exec --name $SRC -- /bin/sync
		freeze=--freeze-source
	fi
	timed_to "$1-branch-s" "$1: branch ${freeze:+$freeze }with 3 published ports" \
		smolvm machine branch --from $SRC --name "$1" $freeze -p "$m:6443" -p "$w:7443" -p "$r:5000"
	now >"$OUT/$1-branched-at"
	vmm_stat "$1" >"$OUT/$1-stat-at-branch"
	sed "s#server: https://.*#server: https://localhost:$m#" "$OUT/mgmt.kubeconfig" >"$OUT/$1-mgmt.kubeconfig"
	sed "s#server: https://.*#server: https://localhost:$w#" "$OUT/work.kubeconfig" >"$OUT/$1-work.kubeconfig"
	metric "$SRC state after branching $1" "$(state $SRC)"
}

readyz() { curl -fsSk --max-time 5 "https://localhost:$1/readyz" >/dev/null; }

k() { # env cluster kubectl-args...
	kubectl --kubeconfig "$OUT/$1-$2.kubeconfig" --request-timeout=30s "${@:3}"
}

deployments_available() { # env
	local ns
	for ns in cert-manager capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system capd-system kapp-controller; do
		k "$1" mgmt -n $ns wait --for=condition=Available deployment --all --timeout=30s || return 1
	done
}

# lease_renewals <env> <cluster> prints the renewTime of every node lease and every leader-election lease.
lease_renewals() {
	local ns jp='{range .items[*]}{.metadata.namespace}/{.metadata.name} {.spec.renewTime}{"\n"}{end}'
	k "$1" "$2" -n kube-node-lease get leases -o jsonpath="$jp"
	k "$1" "$2" -n kube-system get leases kube-controller-manager kube-scheduler -o jsonpath="$jp"
	if [ "$2" = mgmt ]; then
		for ns in capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system capd-system; do
			k "$1" "$2" -n $ns get leases -o jsonpath="$jp"
		done
	fi
}

# leases_renewed <env> <cluster> <epoch>: every lease from lease_renewals was renewed after epoch.
# A restore carries over Ready and Available conditions. A lease renewal shows a live kubelet or controller.
leases_renewed() {
	local renewals name t n=0
	renewals=$(lease_renewals "$1" "$2") || return 1
	echo "$renewals"
	while read -r name t; do
		[ -n "$t" ] && (($(date -d "$t" +%s) > ${3%.*})) || return 1
		n=$((n + 1))
	done <<<"$renewals"
	((n > 0))
}

# gate <name> <t0> <command...>: retries the command for up to 15 min and records when it first passed.
gate() {
	local name=$1 t0=$2
	shift 2
	if retry 900 "$@" >>"$OUT/gate-${name//[^a-zA-Z0-9]/-}.log" 2>&1; then
		metric "$name" "$(since "$t0")"
	else
		metric "$name" "FAILED after $(since "$t0")"
		return 1
	fi
}

# gates <env> <label>: waits for every gate in parallel, timing each from when the branch returned,
# or from now unless the label is "after branch".
gates() {
	local env=$1 t0 m w r p pids=() rc=0 tail_pid
	read -r m w r < <(ports "$env")
	t0=$(now)
	if [ "$2" = "after branch" ]; then t0=$(cat "$OUT/$env-branched-at"); fi
	tail -n0 -f "$OUT/monitor.log" &
	tail_pid=$!
	gate "$env: mgmt /readyz from the host ($2)" "$t0" readyz "$m" &
	pids+=($!)
	gate "$env: work /readyz from the host ($2)" "$t0" readyz "$w" &
	pids+=($!)
	gate "$env: mgmt node Ready ($2)" "$t0" k "$env" mgmt wait --for=condition=Ready nodes --all --timeout=30s &
	pids+=($!)
	gate "$env: work nodes Ready ($2)" "$t0" k "$env" work wait --for=condition=Ready nodes --all --timeout=30s &
	pids+=($!)
	gate "$env: Cluster work Available ($2)" "$t0" k "$env" mgmt -n default wait --for=condition=Available cluster/work --timeout=30s &
	pids+=($!)
	gate "$env: cert-manager, CAPI, CAPD, kapp-controller Deployments Available ($2)" "$t0" deployments_available "$env" &
	pids+=($!)
	gate "$env: mgmt node and leader leases renewed ($2)" "$t0" leases_renewed "$env" mgmt "$t0" &
	pids+=($!)
	gate "$env: work node and leader leases renewed ($2)" "$t0" leases_renewed "$env" work "$t0" &
	pids+=($!)
	for p in "${pids[@]}"; do wait "$p" || rc=1; done
	elapsed "$t0" >"$OUT/$env-gates-s"
	metric "$env: all gates ($2)" "$(cat "$OUT/$env-gates-s") s"
	kill $tail_pid
	return $rc
}

env_up() { # env
	local probe
	branch "$1"
	cpu_probe "$1" "just after branch, during the gates" &
	probe=$!
	gates "$1" "after branch"
	wait $probe
	if [ "$1" = env-a ]; then
		metric "restore to env-a gates: create --from + start + branch + gates" \
			"$(cat "$OUT/create-s" "$OUT/start-s" "$OUT/env-a-branch-s" "$OUT/env-a-gates-s" | awk '{ s += $1 } END { printf "%.1f s", s }')"
	fi
	metric "$1 VMM minor faults, major faults, CPU s from branch to all gates" \
		"$(echo "$(cat "$OUT/$1-stat-at-branch") $(vmm_stat "$1")" | awk '{ printf "%d, %d, %.1f s", $4 - $1, $5 - $2, $6 - $3 }')"
	cpu_probe "$1" "all gates pass"
	guest "$1" churn "$1 after gates"
}

# kubectl_attempt <env> <cluster> <local-port> runs kubectl logs and port-forward against CoreDNS,
# and exec in etcd, as e2e.KubectlWorks does.
kubectl_attempt() {
	local dns etcd pf ok=0
	k "$1" "$2" get nodes -o wide || return 1
	dns=$(k "$1" "$2" -n kube-system get pod -l k8s-app=kube-dns --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}') || return 1
	etcd=$(k "$1" "$2" -n kube-system get pod -l component=etcd -o jsonpath='{.items[0].metadata.name}') || return 1
	k "$1" "$2" -n kube-system logs "$dns" --tail=1 || return 1
	k "$1" "$2" -n kube-system exec "$etcd" -c etcd -- etcd --version || return 1
	k "$1" "$2" -n kube-system port-forward "pod/$dns" "$3:8080" >>"$OUT/port-forward-$1-$2.log" 2>&1 &
	pf=$!
	retry 30 curl -fsS "http://127.0.0.1:$3/health" && ok=1
	kill $pf 2>/dev/null
	wait $pf 2>/dev/null
	echo
	((ok))
}

kubectl_works() { # env cluster local-port
	retry 300 kubectl_attempt "$@"
	metric "$1 $2: kubectl logs, exec and port-forward from the host" ok
}

run_pod() { # env cluster image
	k "$1" "$2" run spike-registry --restart=Never --image="$3" --dry-run=client -o yaml --command -- sleep 3600 |
		k "$1" "$2" apply -f -
}

registry() { # env
	local m w r ref digest c
	read -r m w r < <(ports "$1")
	ref=localhost:$r/spike/busybox:1.37.0
	timed "$1: crane copy busybox into the session registry through the branch port" crane copy --platform linux/amd64 $BUSYBOX "$ref"
	digest=$(crane digest "$ref")
	metric "$1: pushed image digest" "$digest"
	for c in mgmt work; do
		retry 120 run_pod "$1" $c "$GUEST_REGISTRY/spike/busybox@$digest"
		timed "$1 $c: pod from the session registry Ready" k "$1" $c wait --for=condition=Ready pod/spike-registry --timeout=3m
		metric "$1 $c: pod imageID" "$(k "$1" $c get pod spike-registry -o jsonpath='{.status.containerStatuses[0].imageID}')"
	done
}

access() { # env mgmt-local-port work-local-port
	timed "$1: mgmt kubectl checks from the host" kubectl_works "$1" mgmt "$2"
	timed "$1: work kubectl checks from the host" kubectl_works "$1" work "$3"
	registry "$1"
}

# soak: 15 min with both environments up. Every 10 s it samples host memory, each VMM's Pss and state,
# an exec in each environment, and /readyz on every published API port.
soak() {
	local end=$((SECONDS + 900)) log=$OUT/soak.log m p
	: >"$log"
	while ((SECONDS < end)); do
		{
			printf '%s %s' "$(date +%T)" "$(host_mem)"
			for m in $SRC env-a env-b; do printf ' %s=%s/%sMiB' $m "$(state $m)" "$(vmm_smaps $m Pss)"; done
			for m in env-a env-b; do
				if timeout 60 smolvm machine exec --name $m --timeout 20s -- true >/dev/null 2>&1; then
					printf ' exec-%s=ok' $m
				else
					printf ' exec-%s=FAIL' $m
				fi
			done
			for p in 26443 27443 28443 29443; do readyz $p && printf ' %s=ok' $p || printf ' %s=FAIL' $p; done
			echo
		} | tee -a "$log"
		sleep 10
	done
	metric "soak: samples, first to last, largest gap" "$(awk '{ split($1, t, ":"); s = t[1] * 3600 + t[2] * 60 + t[3]
		if (NR == 1) first = s; else if (s - last > gap) gap = s - last; last = s }
		END { printf "%d, %d s, %d s", NR, last - first, gap }' "$log")"
	metric "soak: peak host used, lowest available, peak swap used" "$(awk '{
		for (i = 2; i <= NF; i++) { split($i, kv, "="); v[kv[1]] = kv[2] + 0 }
		if (v["used"] > u) u = v["used"]; if (a == "" || v["avail"] < a) a = v["avail"]; if (v["swap"] > s) s = v["swap"]
	} END { printf "%d MiB, %d MiB, %d MiB", u, a, s }' "$log")"
	for m in $SRC env-a env-b; do
		metric "soak: $m peak Pss, states seen" "$(awk -v m=$m '{ for (i = 2; i <= NF; i++) if (index($i, m "=") == 1) {
			split(substr($i, length(m) + 2), sv, "/"); st[sv[1]] = 1; if (sv[2] + 0 > p) p = sv[2] + 0 } }
			END { printf "%d MiB,", p; for (s in st) printf " %s", s }' "$log")"
	done
	for p in exec-env-a exec-env-b 26443 27443 28443 29443; do
		metric "soak: failed samples of $p" "$(grep -c " $p=FAIL" "$log" || true)"
	done
	# Fails on a failed exec, a VM neither running nor frozen, an API server down for 3 samples in a row,
	# or fewer than 60 samples.
	awk '{
		for (i = 2; i <= NF; i++) {
			split($i, kv, "=")
			if (kv[1] ~ /^(src|env-a|env-b)$/) {
				split(kv[2], sv, "/")
				if (sv[1] != "running" && sv[1] != "frozen") bad = bad " " $1 ":" $i
			} else if (kv[1] ~ /^exec-/ && kv[2] != "ok") {
				bad = bad " " $1 ":" $i
			} else if (kv[1] ~ /^[0-9]+$/) {
				down[kv[1]] = kv[2] == "ok" ? 0 : down[kv[1]] + 1
				if (down[kv[1]] == 3) bad = bad " " $1 ":" kv[1] "-down-3-samples"
			}
		}
	}
	END {
		if (NR < 60) bad = bad " only-" NR "-samples"
		if (bad != "") { print "soak failed:" bad; exit 1 }
	}' "$log"
}

# host_oom_lines counts host kernel OOM lines logged after the restore.
host_oom_lines() {
	sudo dmesg | awk -v t="$(cat "$OUT/host-uptime-at-restore")" -F'[][]' \
		'$2 + 0 >= t && tolower($0) ~ /out of memory|oom-kill|killed process/' | wc -l
}

after_soak() {
	local rc=0 m host
	gates env-a "after soak" || rc=1
	gates env-b "after soak" || rc=1
	guest env-a churn "env-a after soak" || rc=1
	guest env-b churn "env-b after soak" || rc=1
	host=$(host_oom_lines)
	metric "host kernel OOM lines since restore" "$host"
	[ "$host" = 0 ] || rc=1
	for m in env-a env-b; do
		guest $m oom-lines "$(cat "$OUT/guest-uptime-at-restore")" || rc=1
	done
	resources "after soak" env-a || true # diagnostics only
	kill "$(cat "$OUT/monitor.pid")" || true
	return $rc
}

diag() {
	set +e
	local d=$OUT/diag m dir
	mkdir -p "$d"
	sudo dmesg >"$d/host-dmesg.txt"
	smolvm machine ls -v >"$d/machines.txt" 2>&1
	df -h / >"$d/host-df.txt"
	for m in $(smolvm machine ls -q); do
		dir=$(smolvm machine data-dir --name "$m")
		cp "$dir/agent-console.log" "$d/$m-agent-console.log"
		cp "$dir/agent-startup-error.log" "$d/$m-agent-startup-error.log"
		smolvm machine status --name "$m" --json >"$d/$m-status.json"
		if [ "$(jq -r .state "$d/$m-status.json")" = running ]; then
			smolvm machine exec --name "$m" --timeout 10m -- sh /root/guest.sh diag
			smolvm machine cp "$m:/root/diag.tgz" "$d/$m-guest.tgz"
		fi
	done
	return 0
}

case $1 in
host-tools) host_tools ;;
boot) boot ;;
docker) docker_up ;;
mgmt) mgmt ;;
platform) platform ;;
capture) guest $PLAT churn "before capture" && capture plain ;;
zeroed-capture) zeroed_capture ;;
restore) restore "$2" ;;
env) env_up "$2" ;;
recheck) gates "$2" "$3" && guest "$2" churn "$2 $3" ;;
access) access "$2" "$3" "$4" ;;
soak) soak ;;
after-soak) after_soak ;;
summary) summary ;;
diag) diag ;;
*) echo "unknown stage $1" >&2; exit 2 ;;
esac
