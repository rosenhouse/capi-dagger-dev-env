#!/usr/bin/env bash
# Host side of the platform spike: run.sh <stage>. Stages run in workflow order.
# Measurements accumulate in $METRICS; the summary stage writes them to the step summary.
set -euo pipefail
cd "$(dirname "$0")"

SRC=plat # the platform machine; it publishes no ports and becomes the frozen branch source
ENV=env  # a branch of SRC that publishes host ports, as an environment would
H_MGMT=26443 H_WORK=27443 H_REG=25000
GUEST_REGISTRY=172.31.255.254:5000
BUSYBOX=busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
OUT=$RUNNER_TEMP/platform
METRICS=$OUT/metrics.md
mkdir -p "$OUT"

metric() { echo "metric: $1 = $2"; echo "| $1 | $2 |" >>"$METRICS"; }
now() { date +%s.%N; }
since() { awk -v a="$1" -v b="$(now)" 'BEGIN { printf "%.1f s", b - a }'; }

timed() { # name command...
	local name=$1 t0
	shift
	t0=$(now)
	"$@"
	metric "$name" "$(since "$t0")"
}

retry() { # seconds command...
	local end=$((SECONDS + $1))
	shift
	until "$@"; do
		((SECONDS < end)) || return 1
		sleep 2
	done
}

# guest <machine> <stage> [arg]: runs a guest.sh stage and records its metric lines.
guest() {
	local log="$OUT/guest-$2${3:+-$3}.log" rc=0
	smolvm machine exec --name "$1" --stream --timeout 30m -- sh /root/guest.sh "${@:2}" | tee "$log" || rc=$?
	sed -n 's/^metric|\(.*\)|\(.*\)$/| \1 | \2 |/p' "$log" >>"$METRICS"
	return "$rc"
}

host_tools() {
	local bin=$OUT/bin
	mkdir -p "$bin"
	curl -fsSLo "$bin/kubectl" https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl
	echo "6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f  $bin/kubectl" | sha256sum -c -
	chmod +x "$bin/kubectl"
	curl -fsSLo "$OUT/ggcr.tgz" https://github.com/google/go-containerregistry/releases/download/v0.22.1/go-containerregistry_Linux_x86_64.tar.gz
	echo "0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0  $OUT/ggcr.tgz" | sha256sum -c -
	tar -xzf "$OUT/ggcr.tgz" -C "$bin" crane
	echo "$bin" >>"$GITHUB_PATH"
}

boot() {
	metric "runner CPU" "$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs)"
	metric "smolvm" "$(smolvm --version)"
	smolvm machine create --name $SRC --net --net-backend virtio-net --cpus 4 --mem 12288 --storage 40 --overlay 10
	timed "first start of the bare VM (--branchable)" smolvm machine start --name $SRC --branchable
	for f in guest.sh kind-mgmt.yaml kindnet-crs.yaml ../../../internal/devenv/platform/kindnet.yaml; do
		smolvm machine cp "$f" "$SRC:/root/$(basename "$f")"
	done
	timed "guest install (apk, Docker, kind, kubectl, clusterctl, manifests)" guest $SRC install
}

docker_up() {
	guest $SRC setup
	smolvm machine exec --name $SRC -d -- sh /root/guest.sh dockerd
	timed "dockerd ready" guest $SRC docker-facts
	guest $SRC kernel-probe
}

mgmt() {
	timed "kind network and session registry" guest $SRC registry
	timed "docker pull kindest/node" guest $SRC pull
	timed "kind create cluster mgmt, node Ready" guest $SRC kind
}

platform() {
	timed "clusterctl init (cert-manager, CAPI, kubeadm, CAPD)" guest $SRC capi
	timed "kapp-controller rolled out" guest $SRC kapp
	timed "workload manifests applied" guest $SRC workload
	timed "Cluster work Available, 2 nodes Ready" guest $SRC workload-wait
	timed "work API forwarder answers on 7443" guest $SRC forward
}

resources() { # label machine-to-inspect
	local m pid
	free -m
	metric "host memory used, available ($1)" "$(free -m | awk '/^Mem:/ { print $3 " MiB, " $7 " MiB" }')"
	for m in $(smolvm machine ls -q); do
		pid=$(smolvm machine status --name "$m" --json | jq -r .pid)
		grep -E '^(Rss|Pss|Pss_Anon|Pss_Shmem):' "/proc/$pid/smaps_rollup"
		metric "VMM $m Rss, Pss, Pss_Shmem ($1)" \
			"$(awk '/^(Rss|Pss|Pss_Shmem):/ { printf "%s%d MiB", sep, $2 / 1024; sep = ", " }' "/proc/$pid/smaps_rollup")"
		metric "host disk of $m ($1)" "$(du -sm "$(smolvm machine data-dir --name "$m")" | cut -f1) MiB"
	done
	guest "$2" resources "$1"
}

branch() {
	local p
	smolvm machine exec --name $SRC -- /bin/sync
	for p in $H_MGMT $H_WORK $H_REG; do
		if ss -Htln "sport = :$p" | grep -q .; then
			echo "host port $p is in use" >&2
			return 1
		fi
	done
	timed "branch --freeze-source with 3 published ports" smolvm machine branch --from $SRC --name $ENV --freeze-source \
		-p $H_MGMT:6443 -p $H_WORK:7443 -p $H_REG:5000
	metric "source state after branch" "$(smolvm machine status --name $SRC --json | jq -r .state)"
	smolvm machine ls -v
}

readyz() { curl -fsSk --max-time 5 "https://localhost:$1/readyz" >/dev/null; }

kubeconfig() { # guest-path host-port name
	smolvm machine exec --name $ENV -- cat "$1" | sed "s#server: https://.*#server: https://localhost:$2#" >"$OUT/$3.kubeconfig"
}

kubectl_works() { # cluster local-port
	local k=(kubectl --kubeconfig "$OUT/$1.kubeconfig") dns etcd pf
	"${k[@]}" wait --for=condition=Ready nodes --all --timeout=5m
	"${k[@]}" get nodes -o wide
	"${k[@]}" -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-dns --timeout=5m
	dns=$("${k[@]}" -n kube-system get pod -l k8s-app=kube-dns -o jsonpath='{.items[0].metadata.name}')
	etcd=$("${k[@]}" -n kube-system get pod -l component=etcd -o jsonpath='{.items[0].metadata.name}')
	"${k[@]}" -n kube-system logs "$dns" --tail=2
	"${k[@]}" -n kube-system exec "$etcd" -c etcd -- etcd --version
	"${k[@]}" -n kube-system port-forward "pod/$dns" "$2:8080" >"$OUT/port-forward-$1.log" 2>&1 &
	pf=$!
	retry 30 curl -fsS "http://127.0.0.1:$2/health"
	echo
	kill $pf
	metric "$1: kubectl logs, exec and port-forward from the host" ok
}

host_access() {
	timed "mgmt /readyz from the host after branch" retry 300 readyz $H_MGMT
	timed "work /readyz from the host after branch" retry 300 readyz $H_WORK
	kubeconfig /root/.kube/config $H_MGMT mgmt
	kubeconfig /root/work.kubeconfig $H_WORK work
	timed "mgmt kubectl checks from the host" kubectl_works mgmt 18181
	timed "work kubectl checks from the host" kubectl_works work 18182
}

registry() {
	local ref=localhost:$H_REG/spike/busybox:1.37.0 digest c
	timed "crane copy busybox into the session registry through the branch port" crane copy --platform linux/amd64 $BUSYBOX "$ref"
	digest=$(crane digest "$ref")
	metric "pushed image digest" "$digest"
	for c in mgmt work; do
		kubectl --kubeconfig "$OUT/$c.kubeconfig" run spike-registry --restart=Never \
			--image="$GUEST_REGISTRY/spike/busybox@$digest" --command -- sleep 3600
		timed "$c pod from the session registry Ready" \
			kubectl --kubeconfig "$OUT/$c.kubeconfig" wait --for=condition=Ready pod/spike-registry --timeout=3m
		metric "$c pod imageID" "$(kubectl --kubeconfig "$OUT/$c.kubeconfig" get pod spike-registry -o jsonpath='{.status.containerStatuses[0].imageID}')"
	done
}

summary() {
	{
		echo "### Platform spike"
		echo
		echo "| measurement | value |"
		echo "| --- | --- |"
		cat "$METRICS"
	} | tee -a "$GITHUB_STEP_SUMMARY"
}

diag() {
	set +e
	local d=$OUT/diag m dir
	mkdir -p "$d"
	sudo dmesg >"$d/host-dmesg.txt"
	smolvm machine ls -v >"$d/machines.txt" 2>&1
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
resources) resources "$2" "$3" ;;
branch) branch ;;
host-access) host_access ;;
registry) registry ;;
summary) summary ;;
diag) diag ;;
*) echo "unknown stage $1" >&2; exit 2 ;;
esac
