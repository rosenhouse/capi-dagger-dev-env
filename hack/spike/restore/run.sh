#!/usr/bin/env bash
# Host side of the restore spike: run.sh <stage> [arg]. Stages run in workflow order, after the
# platform spike's run.sh has brought the platform up in the machine plat.
# plat is checkpointed and deleted. An environment is then either restored directly from the
# checkpoint (restore), or branched from a source restored with --branchable (branch, branch-copy).
set -euo pipefail
cd "$(dirname "$0")"

OUT=$RUNNER_TEMP/restore
METRICS=$OUT/metrics.md
CKPT=$RUNNER_TEMP/checkpoints/platform.checkpoint
GUEST_LOG=/dev/shm/m1.log
mkdir -p "$OUT" "$(dirname "$CKPT")"
. ../platform/lib.sh

ports() { # machine: host ports of its mgmt API, work API, session registry, and the two port-forward checks
	case $1 in
	env) echo 26443 27443 25000 18181 18182 ;;
	env2) echo 36443 37443 35000 18183 18184 ;;
	*) return 1 ;;
	esac
}

# phase <name> <machine>: starts a measurement window about machine.
phase() { echo "$(now) $1 $2" >>"$OUT/phases.log"; }

# started <machine>: records the machine's VMM pid for the host sampler.
started() { echo "$(now) $1 $(smolvm machine status --name "$1" --json | jq -r .pid)" >>"$OUT/pids.log"; }

timed() { # name command...
	local name=$1 t0
	shift
	t0=$(now)
	"$@"
	metric "$name" "$(since "$t0")"
}

# guest <machine> <guest.sh stage> [arg]: runs a platform guest.sh stage and records its metric lines.
guest() {
	local log rc=0
	log=$OUT/guest-$1-$2-$(date +%s%N).log
	smolvm machine exec --name "$1" --stream --timeout 30m -- sh /root/guest.sh "${@:2}" | tee "$log" || rc=$?
	sed -n 's/^metric|\(.*\)|\(.*\)$/| \1 | \2 |/p' "$log" >>"$METRICS"
	return "$rc"
}

save_guest_log() { # machine
	smolvm machine exec --name "$1" --timeout 5m -- cat $GUEST_LOG >"$OUT/guest-$1.log"
}

resources() { # label
	local m pid
	free -m
	df -h /
	metric "host memory used, available ($1)" "$(free -m | awk '/^Mem:/ { print $3 " MiB, " $7 " MiB" }')"
	metric "host disk used ($1)" "$(df -BG --output=used / | tail -1 | xargs)"
	for m in $(smolvm machine ls -q); do
		pid=$(smolvm machine status --name "$m" --json | jq -r .pid)
		[ "$pid" != null ] || continue
		sudo cat "/proc/$pid/smaps_rollup" >"$OUT/smaps-$m-${1//[^a-z0-9]/-}.txt"
		metric "VMM $m Rss, Pss, Pss_Anon, Pss_File, Pss_Shmem ($1)" \
			"$(awk '/^(Rss|Pss|Pss_Anon|Pss_File|Pss_Shmem):/ { printf "%s%d", sep, $2 / 1024; sep = ", " } END { print " MiB" }' \
				"$OUT/smaps-$m-${1//[^a-z0-9]/-}.txt")"
	done
}

observe() {
	smolvm machine cp sampler-guest.sh plat:/root/sampler-guest.sh
	smolvm machine exec --name plat -d -- sh /root/sampler-guest.sh
	started plat
	sudo nohup ./sampler-host.sh "$OUT" >>"$OUT/sampler-host.err" 2>&1 &
	phase steady plat
	sleep 60
	resources "plat steady"
}

# capture <zero-fill|none>
capture() {
	local log=$OUT/capture.log used t0
	guest plat pre-capture
	if [ "$1" = zero-fill ]; then
		phase zero-fill plat
		guest plat zero-fill
		resources "plat zero-filled"
	fi
	used=$(df -m --output=used / | tail -1)
	phase capture plat
	t0=$(now)
	RUST_LOG=smolvm=info smolvm machine checkpoint --name plat -o "$CKPT" 2>&1 | tee "$log"
	metric "capture ($1): time" "$(since "$t0")"
	metric "capture ($1): file size" "$(stat -c %s "$CKPT" | awk '{ printf "%d MiB", $1 / 1048576 }')"
	metric "capture ($1): host disk added" "$(($(df -m --output=used / | tail -1) - used)) MiB"
	metric "capture ($1): smolvm summary" "$(sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -o "Checkpointed '.*" | sed 's/^[^(]*//')"
	phase after-capture plat
	sleep 150
	resources "plat after capture"
	save_guest_log plat
}

retire() {
	smolvm machine exec --name plat -- cat /root/.kube/config >"$OUT/mgmt.kubeconfig"
	smolvm machine exec --name plat -- cat /root/work.kubeconfig >"$OUT/work.kubeconfig"
	smolvm machine delete --name plat -f
}

kubeconfigs() { # machine
	local m w
	read -r m w _ < <(ports "$1")
	sed "s#server: https://.*#server: https://localhost:$m#" "$OUT/mgmt.kubeconfig" >"$OUT/$1-mgmt.kubeconfig"
	sed "s#server: https://.*#server: https://localhost:$w#" "$OUT/work.kubeconfig" >"$OUT/$1-work.kubeconfig"
}

readyz() { curl -fsSk --max-time 5 "https://localhost:$1/readyz" >/dev/null; }

k() { # machine cluster kubectl-args...
	kubectl --kubeconfig "$OUT/$1-$2.kubeconfig" --request-timeout=30s "${@:3}"
}

deployments_available() { # machine
	local ns
	for ns in cert-manager capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system capd-system kapp-controller; do
		k "$1" mgmt -n $ns wait --for=condition=Available deployment --all --timeout=30s || return 1
	done
}

# gate <name> <t0> <command...>: retries the command for up to 15 min and records when it first passed.
gate() {
	local name=$1 t0=$2
	shift 2
	if retry 900 "$@" >>"$OUT/gate-${name//[^a-z0-9]/-}.log" 2>&1; then
		metric "$name" "$(since "$t0")"
	else
		metric "$name" "FAILED after $(since "$t0")"
		return 1
	fi
}

# kubectl_attempt <machine> <cluster> <local-port> runs kubectl logs and port-forward against
# CoreDNS, and exec in etcd, as e2e.KubectlWorks does.
kubectl_attempt() {
	local dns etcd pf ok=0
	dns=$(k "$1" "$2" -n kube-system get pod -l k8s-app=kube-dns --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}') || return 1
	etcd=$(k "$1" "$2" -n kube-system get pod -l component=etcd -o jsonpath='{.items[0].metadata.name}') || return 1
	k "$1" "$2" -n kube-system logs "$dns" --tail=1 || return 1
	k "$1" "$2" -n kube-system exec "$etcd" -c etcd -- etcd --version || return 1
	k "$1" "$2" -n kube-system port-forward "pod/$dns" "$3:8080" >>"$OUT/port-forward-$1-$2.log" 2>&1 &
	pf=$!
	retry 30 curl -fsS "http://127.0.0.1:$3/health" && ok=1
	kill $pf 2>/dev/null
	wait $pf 2>/dev/null
	((ok))
}

# vmm_times <machine>: the VMM's minor faults, then CPU ticks in guest mode, host kernel and VMM user space.
vmm_times() {
	local pid
	pid=$(smolvm machine status --name "$1" --json | jq -r .pid)
	sudo cat "/proc/$pid/stat" | sed 's/.*) //' | awk '{ print $8, $41, $13, $12 - $41 }'
}

# gates <machine> <t0> <label>: waits for every gate in parallel, timing each from t0.
gates() {
	local m=$1 t0=$2 mp wp mf wf p pids=() rc=0 before
	read -r mp wp _ mf wf < <(ports "$m")
	before=$(vmm_times "$m")
	gate "$m: mgmt /readyz from the host ($3)" "$t0" readyz "$mp" &
	pids+=($!)
	gate "$m: work /readyz from the host ($3)" "$t0" readyz "$wp" &
	pids+=($!)
	gate "$m: mgmt node Ready ($3)" "$t0" k "$m" mgmt wait --for=condition=Ready nodes --all --timeout=30s &
	pids+=($!)
	gate "$m: work nodes Ready ($3)" "$t0" k "$m" work wait --for=condition=Ready nodes --all --timeout=30s &
	pids+=($!)
	gate "$m: Cluster work Available ($3)" "$t0" k "$m" mgmt -n default wait --for=condition=Available cluster/work --timeout=30s &
	pids+=($!)
	gate "$m: cert-manager, CAPI, CAPD, kapp-controller Deployments Available ($3)" "$t0" deployments_available "$m" &
	pids+=($!)
	gate "$m: mgmt kubectl logs, exec, port-forward ($3)" "$t0" kubectl_attempt "$m" mgmt "$mf" &
	pids+=($!)
	gate "$m: work kubectl logs, exec, port-forward ($3)" "$t0" kubectl_attempt "$m" work "$wf" &
	pids+=($!)
	for p in "${pids[@]}"; do wait "$p" || rc=1; done
	metric "$m: all gates ($3)" "$(since "$t0")"
	metric "$m: VMM minor faults, then CPU s in guest mode, host kernel, VMM user space, until all gates ($3)" \
		"$(echo "$before $(vmm_times "$m")" | awk '{ printf "%d, %.1f, %.1f, %.1f", $5 - $1, ($6 - $2) / 100, ($7 - $3) / 100, ($8 - $4) / 100 }')"
	guest "$m" churn "$m $3" || rc=1
	return $rc
}

# restore_env <machine>: restores the checkpoint into machine, publishes its ports, starts it and waits for the gates.
restore_env() {
	local m w r t0
	read -r m w r _ < <(ports "$1")
	phase "create-$1" "$1"
	timed "$1: create --from" smolvm machine create --name "$1" --from "$CKPT"
	smolvm machine update --name "$1" -p "$m:6443" -p "$w:7443" -p "$r:5000"
	kubeconfigs "$1"
	phase "start-$1" "$1"
	t0=$(now)
	timed "$1: start" smolvm machine start --name "$1"
	started "$1"
	metric "$1: guest clock minus host clock after start" "$(clock_offset "$1")"
	gates "$1" "$t0" "after start"
}

soak() { # machine seconds
	phase "soak-$1" "$1"
	sleep "$2"
	guest "$1" churn "$1 after a $2 s soak"
}

restore() {
	restore_env env
	soak env 120
	resources "env up"
}

# second: a second restore of the same checkpoint while env runs.
second() {
	local rc=0 host
	restore_env env2 || rc=1
	gates env "$(now)" "while env2 runs" || rc=1
	resources "env and env2 up"
	host=$(sudo dmesg | grep -ciE 'out of memory|oom-kill|killed process' || true)
	metric "host kernel OOM lines" "$host"
	[ "$host" = 0 ] || rc=1
	guest env oom-lines || rc=1
	guest env2 oom-lines || rc=1
	return $rc
}

# branch <branch|branch-copy>: restores the checkpoint into src with --branchable, then branches env from it.
# branch-copy makes env --branchable too, so env copies its RAM into its own memfd instead of
# mapping src's copy-on-write.
branch() {
	local m w r t0 extra=()
	[ "$1" = branch-copy ] && extra=(--branchable)
	read -r m w r _ < <(ports env)
	phase create-src src
	timed "src: create --from" smolvm machine create --name src --from "$CKPT"
	phase start-src src
	timed "src: start --branchable" smolvm machine start --name src --branchable
	started src
	sleep 90
	save_guest_log src
	resources "src up"
	kubeconfigs env
	phase start-env env
	t0=$(now)
	timed "env: branch --freeze-source ${extra[*]}" \
		smolvm machine branch --from src --name env --freeze-source "${extra[@]}" -p "$m:6443" -p "$w:7443" -p "$r:5000"
	started env
	gates env "$t0" "after branch"
	soak env 120
	resources "env up"
}

report() {
	local m
	sudo pkill -f sampler-host.sh || true
	for m in $(smolvm machine ls -q); do
		if [ "$(smolvm machine status --name "$m" --json | jq -r .state)" = running ]; then save_guest_log "$m" || true; fi
	done
	python3 report.py "$OUT" >"$OUT/report.md"
	cat "$OUT/report.md" >>"$GITHUB_STEP_SUMMARY"
}

case $1 in
observe) observe ;;
capture) capture "$2" ;;
retire) retire ;;
restore) restore ;;
second) second ;;
branch) branch "$2" ;;
report) report ;;
summary) summary ;;
*) echo "unknown stage $1" >&2; exit 2 ;;
esac
