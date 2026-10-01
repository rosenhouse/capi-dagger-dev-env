#!/bin/sh
# Runs as root in the smolvm guest: sh /root/guest.sh <stage>.
# Lines starting with "metric|" are measurements that run.sh records.
set -eu
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=/root

CERTS=/etc/devenv/certs.d
REGISTRY=172.31.255.254:5000
KIND_NODE=kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
REGISTRY_IMAGE=registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
SOCAT_IMAGE=alpine/socat:1.8.0.3@sha256:beb4a68d9e4fe6b0f21ea774a0fde6c31f580dde6368939ed70100c5385b015e
CAPI=v1.14.2
CERT_MANAGER=v1.21.1

metric() { echo "metric|$1|$2"; }
now() { cut -d' ' -f1 /proc/uptime; }
since() { awk -v a="$1" -v b="$(now)" 'BEGIN { printf "%.1f s", b - a }'; }

retry() { # seconds command...
	end=$(($(date +%s) + $1))
	shift
	until "$@"; do
		[ "$(date +%s)" -lt $end ] || return 1
		sleep 2
	done
}

nodes_ready() { # kubeconfig count
	[ "$(kubectl --kubeconfig "$1" get nodes --no-headers 2>/dev/null | awk '$2 == "Ready"' | wc -l)" = "$2" ]
}

fetch() { # url sha256 dest
	curl -fsSL --retry 5 --retry-all-errors -o "$3" "$1"
	echo "$2  $3" | sha256sum -c -
}

install() {
	retry 120 apk add -q curl iptables nftables conntrack-tools
	t0=$(now)
	fetch https://download.docker.com/linux/static/stable/x86_64/docker-29.8.2.tgz \
		995d1ef289677f74fd58d8d2c35727b6a4ee389c69db8638a3e42d0487aa5b0f /tmp/docker.tgz
	metric "guest download of docker-29.8.2.tgz (85.6 MB)" "$(since "$t0")"
	tar -xzf /tmp/docker.tgz -C /tmp
	mv /tmp/docker/* /usr/local/bin/
	fetch https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-linux-amd64 \
		aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d /usr/local/bin/kind
	fetch https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl \
		6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f /usr/local/bin/kubectl
	fetch https://github.com/kubernetes-sigs/cluster-api/releases/download/$CAPI/clusterctl-linux-amd64 \
		01122674fd3c47a33206ab1b8b81d437afbcf5dd25d126535564f24a2cdf676e /usr/local/bin/clusterctl
	chmod +x /usr/local/bin/kind /usr/local/bin/kubectl /usr/local/bin/clusterctl
	fetch https://github.com/carvel-dev/kapp-controller/releases/download/v0.60.9/release.yml \
		19a2984ba32d5992195886465d7d48dde8dab2bd9a3d9b04ff6ee1d8dea60e70 /root/kapp-controller.yaml
	clusterctl_repository
}

# The local repository that platform.go's clusterctlRepository builds.
clusterctl_repository() {
	release=https://github.com/kubernetes-sigs/cluster-api/releases/download/$CAPI
	fetch $release/metadata.yaml c470906f551ac3e3e9aedc9a3e733b5a5994428693df43e95f65ea67c035f9fe /tmp/metadata.yaml
	fetch $release/clusterclass-quick-start.yaml 85d414afc4af00e4ff4472cec46ab49a6aa85cb839a771b44ab0a6c7f6c8e24e /root/clusterclass-quick-start.yaml
	while read -r dir file sum; do
		mkdir -p /repo/$dir/$CAPI
		fetch $release/$file "$sum" /repo/$dir/$CAPI/$file
		cp /tmp/metadata.yaml /repo/$dir/$CAPI/metadata.yaml
	done <<-EOF
		cluster-api core-components.yaml b2fff42cb5e35440ed963a463c5ab128004481b5a4336ea0005812be2ff7e2a8
		bootstrap-kubeadm bootstrap-components.yaml 2a2d24f83244a6dae60e35d9e72e93c6ac3c209eedc467184737bb8eecfd63bb
		control-plane-kubeadm control-plane-components.yaml 7aa827b43eee898d8597bb39b7db5cf755c872c7a4da68b05cc3a9f35c459c93
		infrastructure-docker infrastructure-components-development.yaml f6aef43bf70b76a38b1628cd4349d291159fbfd8082dbe9c2ec13d04487b5867
		infrastructure-docker cluster-template-development.yaml 7dd5249f22a4ce0fbe667d1c764dd5919775f81847bceb2aa16fc5acb138b99b
	EOF
	mkdir -p /repo/cert-manager/$CERT_MANAGER
	fetch https://github.com/cert-manager/cert-manager/releases/download/$CERT_MANAGER/cert-manager.yaml \
		5f6a499b8c1857d57f560f536e0dcc830914b45c420899fe7ad0692c8624e408 /repo/cert-manager/$CERT_MANAGER/cert-manager.yaml
	cat >/repo/clusterctl.yaml <<-EOF
		providers:
		- name: cluster-api
		  type: CoreProvider
		  url: /repo/cluster-api/$CAPI/core-components.yaml
		- name: kubeadm
		  type: BootstrapProvider
		  url: /repo/bootstrap-kubeadm/$CAPI/bootstrap-components.yaml
		- name: kubeadm
		  type: ControlPlaneProvider
		  url: /repo/control-plane-kubeadm/$CAPI/control-plane-components.yaml
		- name: docker
		  type: InfrastructureProvider
		  url: /repo/infrastructure-docker/$CAPI/infrastructure-components-development.yaml
		cert-manager:
		  url: /repo/cert-manager/$CERT_MANAGER/cert-manager.yaml
		  version: $CERT_MANAGER
	EOF
}

# Mounts and sysctls do not survive a stop and start, so this runs after every cold start.
setup() {
	mkdir -p /storage/docker /var/lib/docker /storage/containerd /var/lib/containerd /lib/modules $CERTS
	mountpoint -q /var/lib/docker || mount --bind /storage/docker /var/lib/docker
	mountpoint -q /var/lib/containerd || mount --bind /storage/containerd /var/lib/containerd
	mount --make-rshared /
	sysctl -w fs.inotify.max_user_instances=8192
	sysctl -w fs.inotify.max_user_watches=1048576
	rm -f /var/run/docker.pid /var/run/docker/containerd/containerd.pid
}

start_dockerd() {
	exec dockerd --storage-driver=overlay2 >>/var/log/dockerd.log 2>&1
}

docker_facts() {
	retry 120 docker info >/dev/null 2>&1
	metric "docker server" "$(docker version --format '{{.Server.Version}} (API {{.Server.APIVersion}})')"
	metric "docker storage driver" "$(docker info --format '{{.Driver}}') on $(findmnt -no FSTYPE -T /var/lib/docker)"
	metric "docker cgroup" "v$(docker info --format '{{.CgroupVersion}} {{.CgroupDriver}}')"
	metric "iptables" "$(iptables -V)"
	metric "root cgroup.subtree_control" "$(cat /sys/fs/cgroup/cgroup.subtree_control)"
	metric "guest kernel" "$(uname -r)"
}

kernel_probe() {
	set +e
	out=$(probe 2>&1)
	echo "$out"
	metric "kernel probe ok lines" "$(echo "$out" | grep -c '^ok')"
	fails=$(echo "$out" | sed -n 's/^FAIL *//p' | tr '\n' ';')
	metric "kernel probe FAILs" "$fails"
	expected="-m recent --name x --rcheck;-m physdev --physdev-is-bridged;ct label;queue;ctnetlink;"
	if [ "$fails" = "$expected" ] && [ "$(echo "$out" | grep -c '^ok')" = 13 ]; then
		metric "kernel probe matches expectations" yes
	else
		metric "kernel probe matches expectations" NO
	fi
}

probe() {
	uname -r
	cat /proc/cmdline
	cat /sys/fs/cgroup/cgroup.controllers /sys/fs/cgroup/cgroup.subtree_control
	findmnt -no FSTYPE,OPTIONS /sys/fs/cgroup
	test -c /dev/kmsg && echo kmsg-ok
	sysctl net.bridge.bridge-nf-call-iptables fs.inotify.max_user_instances fs.inotify.max_user_watches net.netfilter.nf_conntrack_max
	iptables -V
	unshare -n sh -c '
	 for r in "-m conntrack --ctstate NEW" "-m comment --comment x" "-m mark --mark 0x4000/0x4000" \
	          "-m addrtype --dst-type LOCAL" "-m statistic --mode random --probability 0.5" \
	          "-p tcp -m multiport --dports 1,2" "-m recent --name x --rcheck" "-m physdev --physdev-is-bridged"; do
	   iptables -A INPUT $r -j ACCEPT 2>/dev/null && echo "ok   $r" || echo "FAIL $r"; done
	 iptables -t nat -A POSTROUTING -j MASQUERADE --random-fully && echo ok-masq
	 iptables -t nat -A PREROUTING -p tcp -j DNAT --to-destination 10.0.0.1:80 && echo ok-dnat
	 iptables -t mangle -A PREROUTING -j MARK --or-mark 0x4000 && echo ok-MARK
	 iptables -t raw -A PREROUTING -j DROP && echo ok-raw
	 ip6tables -t nat -A POSTROUTING -j MASQUERADE && echo ok-masq6
	 nft add table inet t && nft add chain inet t c "{ type filter hook postrouting priority 0; }" &&
	 nft add rule inet t c numgen random mod 2 vmap { 0 : accept, 1 : accept } && echo ok-numgen
	 nft add rule inet t c fib daddr type local accept && echo ok-fib
	 nft add rule inet t c ct label set 1 2>/dev/null && echo ok-ctlabel || echo "FAIL ct label"
	 nft add rule inet t c queue num 101 bypass 2>/dev/null && echo ok-queue || echo "FAIL queue"
	'
	conntrack -L >/dev/null 2>&1 && echo ok-ctnetlink || echo "FAIL ctnetlink"
}

# The session registry sits on the kind network at a fixed IP outside Docker's --ip-range.
registry() {
	docker network create -d bridge -o com.docker.network.bridge.enable_ip_masquerade=true \
		-o com.docker.network.driver.mtu=1500 --subnet 172.31.0.0/16 --ip-range 172.31.0.0/17 kind
	docker run -d --name devenv-registry --restart=always --network kind --ip ${REGISTRY%:*} -p 5000:5000 $REGISTRY_IMAGE
	mkdir -p $CERTS/$REGISTRY
	printf 'server = "http://%s"\n\n[host."http://%s"]\n  capabilities = ["pull", "resolve"]\n' $REGISTRY $REGISTRY >$CERTS/$REGISTRY/hosts.toml
	retry 60 curl -fsS http://$REGISTRY/v2/
	echo
}

pull() {
	docker pull -q $KIND_NODE
}

kind_create() {
	kind create cluster --name mgmt --image $KIND_NODE --config /root/kind-mgmt.yaml
	kubectl wait --for=condition=Ready nodes --all --timeout=5m
	kubectl get nodes -o wide
}

capi() {
	CLUSTER_TOPOLOGY=true CLUSTERCTL_DISABLE_VERSIONCHECK=true clusterctl init --config /repo/clusterctl.yaml \
		--core cluster-api:$CAPI --bootstrap kubeadm:$CAPI --control-plane kubeadm:$CAPI --infrastructure docker:$CAPI \
		--wait-providers --wait-provider-timeout 600
}

kapp() {
	kubectl apply --server-side -f /root/kapp-controller.yaml >/dev/null
	kubectl -n kapp-controller rollout status deployment/kapp-controller --timeout=5m
}

# What platform.go's CreateWorkloadCluster applies, rendered with sed and kubectl instead of Go.
workload() {
	awk '{ print } $0 == "            hostPath: \"/var/run/docker.sock\"" {
		print "          - containerPath: /etc/containerd/certs.d"
		print "            hostPath: /etc/devenv/certs.d"
	}' /root/clusterclass-quick-start.yaml >/root/clusterclass.yaml
	test "$(grep -c 'hostPath: /etc/devenv/certs.d' /root/clusterclass.yaml)" = 2
	kubectl apply --server-side -n default -f /root/clusterclass.yaml
	sed "s|'\${DOCKER_POD_CIDRS},\${DOCKER_POD_IPV6_CIDRS}'|192.168.0.0/16|" /root/kindnet.yaml >/root/kindnet-rendered.yaml
	grep -q 'value: 192.168.0.0/16' /root/kindnet-rendered.yaml
	kubectl create configmap kindnet -n default --from-file=kindnet.yaml=/root/kindnet-rendered.yaml --dry-run=client -o yaml |
		kubectl apply --server-side -f -
	kubectl apply --server-side -f /root/kindnet-crs.yaml
	POD_CIDR='["192.168.0.0/16"]' clusterctl generate cluster work --config /repo/clusterctl.yaml \
		--from /repo/infrastructure-docker/$CAPI/cluster-template-development.yaml \
		--kubernetes-version v1.37.0 --control-plane-machine-count 1 --worker-machine-count 1 --target-namespace default |
		kubectl apply -f -
	kubectl -n default label cluster work cni=kindnet --overwrite
}

workload_wait() {
	kubectl -n default wait --for=condition=Available cluster/work --timeout=25m
	kubectl -n default get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d >/root/work.kubeconfig
	retry 600 nodes_ready /root/work.kubeconfig 2
	kubectl --kubeconfig /root/work.kubeconfig get nodes -o wide
}

# infra.ForwardWorkloadAPI: CAPD's load balancer has a random port, so socat gives it 7443.
forward() {
	docker run -d --name work-api-forward --network kind -p 7443:6443 $SOCAT_IMAGE \
		TCP-LISTEN:6443,fork,reuseaddr TCP:work-lb:6443
	retry 60 curl -fsSk https://127.0.0.1:7443/readyz
	echo
}

resources() {
	free -m
	df -m / /storage
	docker system df
	docker stats --no-stream --format '{{.Name}} {{.MemUsage}}'
	metric "guest memory used, buff/cache ($1)" "$(free -m | awk '/^Mem:/ { print $3 " MiB, " $6 " MiB of " $2 " MiB" }')"
	metric "guest /storage used ($1)" "$(df -m /storage | awk 'NR == 2 { print $3 " MiB" }')"
	metric "guest root overlay used ($1)" "$(df -m / | awk 'NR == 2 { print $3 " MiB" }')"
}

# Runs before each capture. The capture syncs again, and that sync must finish within 30 s.
pre_capture() {
	sync
	fstrim -v /storage
	fstrim -v / || true
}

# Counters that show churn after a restore or branch: container restarts, leader changes,
# nodes going NotReady, and Machines replaced by MachineHealthCheck remediation.
churn() { # label
	for c in mgmt work; do
		kc=/root/.kube/config
		[ $c = mgmt ] || kc=/root/work.kubeconfig
		metric "$c: container restarts, lease transitions, NodeNotReady events, LeaderElection events ($1)" \
			"$(restarts --kubeconfig $kc), $(lease_transitions $kc), $(events $kc NodeNotReady), $(events $kc LeaderElection)"
		metric "$c: pods that restarted ($1)" "$(kubectl --kubeconfig $kc get pods -A --no-headers |
			awk '$5 > 0 { printf "%s%s %d", sep, $2, $5; sep = ", " }')"
		metric "$c: Warning events by reason ($1)" "$(kubectl --kubeconfig $kc get events -A --field-selector type=Warning \
			--no-headers -o custom-columns=:.reason | sort | uniq -c | sort -rn | awk '{ printf "%s%s %d", sep, $2, $1; sep = ", " }')"
	done
	metric "Machines ($1)" "$(kubectl -n default get machines --no-headers -o custom-columns=:.metadata.name,:.status.phase | tr -s ' \n' ' ')"
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

# Samples guest CPU for 10 s: the split from /proc/stat, and the busiest processes.
cpu_probe() {
	head -1 /proc/stat >/tmp/stat0
	cpu_ticks >/tmp/ticks0
	sleep 10
	head -1 /proc/stat >/tmp/stat1
	cpu_ticks >/tmp/ticks1
	metric "guest CPU user, system, iowait, irq, softirq, idle, steal ($1)" "$(cat /tmp/stat0 /tmp/stat1 | awk '
		NR == 1 { for (i = 2; i <= 9; i++) a[i] = $i; next }
		{ for (i = 2; i <= 9; i++) { d[i] = $i - a[i]; t += d[i] } }
		END { printf "%d%%, %d%%, %d%%, %d%%, %d%%, %d%%, %d%%", 100*d[2]/t, 100*d[4]/t, 100*d[6]/t, 100*d[7]/t, 100*d[8]/t, 100*d[5]/t, 100*d[9]/t }')"
	metric "busiest guest processes@container, CPU s in 10 s ($1)" "$(busiest)"
	dmesg | tail -5
}

cpu_ticks() { # pid utime+stime comm
	cat /proc/[0-9]*/stat 2>/dev/null | sed 's/^\([0-9]*\) (\(.*\)) /\1 \2 /' |
		awk '{ n = NF; print $1, $(n - 38) + $(n - 37), $2 }'
}

busiest() {
	awk 'NR == FNR { a[$1] = $2; next } ($1 in a) && $2 > a[$1] { print $2 - a[$1], $1, $3 }' /tmp/ticks0 /tmp/ticks1 |
		sort -rn | head -6 | while read -r ticks pid comm; do
			echo "$ticks $comm@$(container_of "$pid")"
		done | awk '{ printf "%s%s %.1f", sep, $2, $1 / 100; sep = ", " }'
}

container_of() { # pid
	id=$(sed -n 's|^0::/docker/\([0-9a-f]\{12\}\).*|\1|p' "/proc/$1/cgroup" 2>/dev/null)
	if [ -n "$id" ]; then docker ps --filter "id=$id" --format '{{.Names}}'; else echo vm; fi
}

restarts() {
	kubectl "$@" get pods -A --no-headers | awk '{ s += $5 } END { print s }'
}

diag() {
	set +e
	d=/root/diag
	rm -rf $d /root/diag.tgz
	mkdir -p $d
	dmesg >$d/dmesg.txt
	cp /var/log/dockerd.log $d/
	docker ps -a >$d/docker-ps.txt
	for c in $(kind get clusters); do kind export logs $d/kind-$c --name $c; done
	kubectl get clusters,machines,kubeadmcontrolplanes,machinedeployments,devclusters,devmachines -A -o yaml >$d/capi.yaml
	kubectl get pods -A -o wide >$d/mgmt-pods.txt
	kubectl --kubeconfig /root/work.kubeconfig get pods -A -o wide >$d/work-pods.txt
	tar -czf /root/diag.tgz -C /root diag
}

case $1 in
install) install ;;
setup) setup ;;
dockerd) start_dockerd ;;
docker-facts) docker_facts ;;
kernel-probe) kernel_probe ;;
registry) registry ;;
pull) pull ;;
kind) kind_create ;;
capi) capi ;;
kapp) kapp ;;
workload) workload ;;
workload-wait) workload_wait ;;
forward) forward ;;
resources) resources "$2" ;;
pre-capture) pre_capture ;;
churn) churn "$2" ;;
oom-lines) oom_lines ;;
cpu-probe) cpu_probe "$2" ;;
diag) diag ;;
*) echo "unknown stage $1" >&2; exit 2 ;;
esac
