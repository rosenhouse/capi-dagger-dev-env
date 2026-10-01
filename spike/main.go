// Throwaway spike for issue #2. Not merged.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"dagger.io/dagger"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	kindVersion           = "v0.33.0"
	k8sVersion            = "v1.37.0"
	capiVersion           = "v1.14.2"
	certManagerVersion    = "v1.21.1"
	kappControllerVersion = "v0.60.9"
)

//go:embed kindnet.yaml
var kindnet string

var (
	start = time.Now()
	last  = time.Now()
	until = flag.String("until", "all", "last step to run: registry|kind|kapp|capd|all")
)

func step(name string) {
	fmt.Fprintf(os.Stderr, "\n=== [total %6.1fs, prev step %6.1fs] %s\n", time.Since(start).Seconds(), time.Since(last).Seconds(), name)
	last = time.Now()
}

type spike struct {
	c        *dagger.Client
	arch     string
	reg      string // host:port
	mirror   string // host:port
	dind     *dagger.Service
	tools    *dagger.Container
	mgmtKC   string
	workKC   string
	hostInfo string
}

func main() {
	flag.Parse()
	ctx := context.Background()
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	must(err)
	defer c.Close()
	s := &spike{c: c}

	step("host info")
	fmt.Println(hostInfo())

	platform, err := c.DefaultPlatform(ctx)
	must(err)
	s.arch = strings.TrimPrefix(string(platform), "linux/")
	fmt.Println("engine platform:", platform)

	s.registries(ctx)
	s.startDind(ctx)
	s.dindChecks(ctx)
	if *until == "registry" {
		return
	}
	s.kind(ctx)
	if *until == "kind" {
		return
	}
	s.kapp(ctx)
	if *until == "kapp" {
		return
	}
	s.capd(ctx)
	if *until == "capd" {
		return
	}
	s.workload(ctx)
	s.tunnels(ctx)
	step("SPIKE PASSED")
}

func (s *spike) registries(ctx context.Context) {
	step("session domain")
	domain := strings.TrimSpace(s.exec(ctx, s.c.Container().From("alpine:3.22").WithEnvVariable("NONCE", nonce()),
		"awk '/^search/ {print $2}' /etc/resolv.conf"))
	fmt.Println("session domain:", domain)

	step("start registry and docker.io mirror")
	reg, err := s.c.Container().From("registry:3").WithExposedPort(5000).AsService().Start(ctx)
	must(err)
	mirror, err := s.c.Container().From("registry:3").
		WithEnvVariable("REGISTRY_PROXY_REMOTEURL", "https://registry-1.docker.io").
		WithMountedCache("/var/lib/registry", s.c.CacheVolume("spike-mirror-docker.io")).
		WithExposedPort(5000).AsService().Start(ctx)
	must(err)
	s.reg = fqdn(ctx, reg, domain) + ":5000"
	s.mirror = fqdn(ctx, mirror, domain) + ":5000"
	fmt.Println("registry:", s.reg, "mirror:", s.mirror)

	step("push probe images with crane")
	probe := s.c.Container().From("alpine:3.22").WithNewFile("/hello", "hello from session registry\n")
	configImg := s.c.Container().WithRootfs(s.c.Directory().WithNewFile("cm.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: spike-fetched
  namespace: default
data:
  ok: "true"
`))
	crane := s.c.Container().From("gcr.io/go-containerregistry/crane:debug").
		WithServiceBinding("registry", reg).
		WithMountedFile("/probe.tar", probe.AsTarball()).
		WithMountedFile("/config.tar", configImg.AsTarball()).
		WithEnvVariable("NONCE", nonce())
	fmt.Println(s.exec(ctx, crane, fmt.Sprintf("crane push --insecure /probe.tar %[1]s/probe:1 && crane push --insecure /config.tar %[1]s/spike-config:1 && crane catalog --insecure %[1]s", s.reg)))
}

func (s *spike) startDind(ctx context.Context) {
	step("start dind")
	hostsToml := fmt.Sprintf("server = \"http://%[1]s\"\n\n[host.\"http://%[1]s\"]\n  capabilities = [\"pull\", \"resolve\", \"push\"]\n", s.reg)
	dockerHubToml := fmt.Sprintf("server = \"https://registry-1.docker.io\"\n\n[host.\"http://%s\"]\n  capabilities = [\"pull\", \"resolve\"]\n", s.mirror)
	dind, err := s.c.Container().From("docker:29-dind").
		WithEnvVariable("DOCKER_TLS_CERTDIR", "").
		WithNewFile("/etc/devenv/certs.d/"+s.reg+"/hosts.toml", hostsToml).
		WithNewFile("/etc/devenv/certs.d/docker.io/hosts.toml", dockerHubToml).
		WithMountedCache("/var/lib/docker", s.c.CacheVolume("spike-dind"), dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		WithExposedPort(2375).
		AsService(dagger.ContainerAsServiceOpts{
			UseEntrypoint:            true,
			InsecureRootCapabilities: true,
			Args: []string{
				"--tls=false",
				"--insecure-registry=" + s.reg,
				"--insecure-registry=" + s.mirror,
				"--registry-mirror=http://" + s.mirror,
			},
		}).Start(ctx)
	must(err)
	s.dind = dind

	bin := func(url string) *dagger.File { return s.c.HTTP(url) }
	s.tools = s.c.Container().From("docker:29-cli").
		WithFile("/usr/local/bin/kind", bin("https://github.com/kubernetes-sigs/kind/releases/download/"+kindVersion+"/kind-linux-"+s.arch), dagger.ContainerWithFileOpts{Permissions: 0o755}).
		WithFile("/usr/local/bin/kubectl", bin("https://dl.k8s.io/release/"+k8sVersion+"/bin/linux/"+s.arch+"/kubectl"), dagger.ContainerWithFileOpts{Permissions: 0o755}).
		WithFile("/usr/local/bin/clusterctl", bin("https://github.com/kubernetes-sigs/cluster-api/releases/download/"+capiVersion+"/clusterctl-linux-"+s.arch), dagger.ContainerWithFileOpts{Permissions: 0o755}).
		WithDirectory("/repo", s.capiRepo()).
		WithServiceBinding("docker", dind).
		WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
		WithEnvVariable("NONCE", nonce())
}

func (s *spike) dindChecks(ctx context.Context) {
	step("dind: wipe stale state, pull from registry and mirror")
	fmt.Println(s.exec(ctx, s.tools, `set -x
docker info --format '{{.ServerVersion}} cgroup={{.CgroupVersion}} driver={{.Driver}} mirrors={{.RegistryConfig.Mirrors}}'
docker rm -f $(docker ps -aq) 2>/dev/null || true
docker network rm kind 2>/dev/null || true
docker pull `+s.reg+`/probe:1
docker run --rm `+s.reg+`/probe:1 cat /hello
docker pull busybox:1.37
wget -qO- http://`+s.mirror+`/v2/_catalog`))
}

func (s *spike) kind(ctx context.Context) {
	step("kind create mgmt")
	cfg := `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: 6443
nodes:
- role: control-plane
  extraMounts:
  - hostPath: /var/run/docker.sock
    containerPath: /var/run/docker.sock
  - hostPath: /etc/devenv/certs.d
    containerPath: /etc/containerd/certs.d
kubeadmConfigPatches:
- |
  kind: ClusterConfiguration
  apiServer:
    certSANs: [docker]
`
	kc := s.exec(ctx, s.tools.WithNewFile("/kind.yaml", cfg),
		"kind create cluster --name mgmt --image kindest/node:"+k8sVersion+" --config /kind.yaml --wait 5m >&2 && kind get kubeconfig --name mgmt")
	s.mgmtKC = strings.ReplaceAll(kc, "https://0.0.0.0:6443", "https://docker:6443")
	s.tools = s.tools.WithNewFile("/root/.kube/config", s.mgmtKC)

	step("mgmt: nodes, and containerd pulls from registry and mirror")
	fmt.Println(s.exec(ctx, s.tools, `set -x
kubectl get nodes -o wide
docker exec mgmt-control-plane cat /etc/resolv.conf
docker exec mgmt-control-plane crictl pull `+s.reg+`/probe:1
docker exec mgmt-control-plane crictl pull docker.io/library/busybox:1.36
wget -qO- http://`+s.mirror+`/v2/_catalog`))
}

func (s *spike) kapp(ctx context.Context) {
	step("kapp-controller install and fetch from session registry")
	app := fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata: {name: spike-sa, namespace: default}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: spike-sa}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects: [{kind: ServiceAccount, name: spike-sa, namespace: default}]
---
apiVersion: kappctrl.k14s.io/v1alpha1
kind: App
metadata: {name: spike-fetch, namespace: default}
spec:
  serviceAccountName: spike-sa
  fetch:
  - image: {url: %s/spike-config:1}
  template:
  - ytt: {}
  deploy:
  - kapp: {}
`, s.reg)
	fmt.Println(s.exec(ctx, s.tools.
		WithFile("/kapp-controller.yaml", s.c.HTTP("https://github.com/carvel-dev/kapp-controller/releases/download/"+kappControllerVersion+"/release.yml")).
		WithNewFile("/app.yaml", app), `set -x
kubectl apply -f /kapp-controller.yaml >/dev/null
kubectl -n kapp-controller rollout status deploy/kapp-controller --timeout=5m
kubectl apply -f /app.yaml
kubectl wait app/spike-fetch --for=condition=ReconcileSucceeded --timeout=3m || { kubectl get app spike-fetch -o yaml; exit 1; }
kubectl get configmap spike-fetched -o jsonpath='{.data.ok}'`))
}

func (s *spike) capiRepo() *dagger.Directory {
	rel := func(f string) *dagger.File {
		return s.c.HTTP("https://github.com/kubernetes-sigs/cluster-api/releases/download/" + capiVersion + "/" + f)
	}
	meta := rel("metadata.yaml")
	return s.c.Directory().
		WithFile("cluster-api/"+capiVersion+"/core-components.yaml", rel("core-components.yaml")).
		WithFile("cluster-api/"+capiVersion+"/metadata.yaml", meta).
		WithFile("bootstrap-kubeadm/"+capiVersion+"/bootstrap-components.yaml", rel("bootstrap-components.yaml")).
		WithFile("bootstrap-kubeadm/"+capiVersion+"/metadata.yaml", meta).
		WithFile("control-plane-kubeadm/"+capiVersion+"/control-plane-components.yaml", rel("control-plane-components.yaml")).
		WithFile("control-plane-kubeadm/"+capiVersion+"/metadata.yaml", meta).
		WithFile("infrastructure-docker/"+capiVersion+"/infrastructure-components-development.yaml", rel("infrastructure-components-development.yaml")).
		WithFile("infrastructure-docker/"+capiVersion+"/metadata.yaml", meta).
		WithFile("infrastructure-docker/"+capiVersion+"/cluster-template-development.yaml", rel("cluster-template-development.yaml")).
		WithFile("infrastructure-docker/"+capiVersion+"/clusterclass-quick-start.yaml", rel("clusterclass-quick-start.yaml")).
		WithFile("cert-manager/"+certManagerVersion+"/cert-manager.yaml", s.c.HTTP("https://github.com/cert-manager/cert-manager/releases/download/"+certManagerVersion+"/cert-manager.yaml")).
		WithNewFile("clusterctl.yaml", fmt.Sprintf(`providers:
- name: cluster-api
  type: CoreProvider
  url: /repo/cluster-api/%[1]s/core-components.yaml
- name: kubeadm
  type: BootstrapProvider
  url: /repo/bootstrap-kubeadm/%[1]s/bootstrap-components.yaml
- name: kubeadm
  type: ControlPlaneProvider
  url: /repo/control-plane-kubeadm/%[1]s/control-plane-components.yaml
- name: docker
  type: InfrastructureProvider
  url: /repo/infrastructure-docker/%[1]s/infrastructure-components-development.yaml
cert-manager:
  url: /repo/cert-manager/%[2]s/cert-manager.yaml
  version: %[2]s
`, capiVersion, certManagerVersion))
}

func (s *spike) capd(ctx context.Context) {
	step("clusterctl init")
	fmt.Println(s.exec(ctx, s.tools.WithEnvVariable("CLUSTER_TOPOLOGY", "true").WithEnvVariable("GOPROXY", "off"),
		"clusterctl init --config /repo/clusterctl.yaml --core cluster-api:"+capiVersion+" --bootstrap kubeadm:"+capiVersion+
			" --control-plane kubeadm:"+capiVersion+" --infrastructure docker:"+capiVersion+" --wait-providers --wait-provider-timeout 600"))
}

func (s *spike) workload(ctx context.Context) {
	step("create workload cluster")
	crs := `apiVersion: v1
kind: ConfigMap
metadata: {name: cni-work, namespace: default}
data:
  kindnet.yaml: |
` + indent(strings.ReplaceAll(kindnet, "${DOCKER_POD_CIDRS},${DOCKER_POD_IPV6_CIDRS}", "192.168.0.0/16"), "    ") + `
---
apiVersion: addons.cluster.x-k8s.io/v1beta2
kind: ClusterResourceSet
metadata: {name: cni-work, namespace: default}
spec:
  strategy: ApplyOnce
  clusterSelector:
    matchLabels: {cni: kindnet}
  resources:
  - {name: cni-work, kind: ConfigMap}
`
	dir := "/repo/infrastructure-docker/" + capiVersion
	t := s.tools.WithNewFile("/crs.yaml", crs)
	fmt.Println(s.exec(ctx, t, `set -x
kubectl apply -f `+dir+`/clusterclass-quick-start.yaml
kubectl apply -f /crs.yaml
clusterctl generate cluster work --config /repo/clusterctl.yaml --from `+dir+`/cluster-template-development.yaml \
  --kubernetes-version `+k8sVersion+` --control-plane-machine-count 1 --worker-machine-count 0 --target-namespace default > /work.yaml
kubectl apply -f /work.yaml
kubectl label cluster work cni=kindnet
kubectl wait cluster/work --for=condition=ControlPlaneInitialized --timeout=10m || { kubectl get cluster,kcp,machines,dockermachines -A -o wide; kubectl -n capd-system logs deploy/capd-controller-manager --tail=80; exit 1; }
`))

	step("workload: forwarder, API, node Ready, registry pull from CAPD node")
	kc := s.exec(ctx, s.tools, "kubectl get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d")
	t = s.tools.WithNewFile("/work.kubeconfig", kc)
	fmt.Println(s.exec(ctx, t, `set -x
docker ps --format '{{.Names}} {{.Ports}}'
grep server: /work.kubeconfig
docker rm -f work-api-fwd 2>/dev/null || true
docker run -d --name work-api-fwd --network kind -p 7443:6443 alpine/socat TCP-LISTEN:6443,fork,reuseaddr TCP:work-lb:6443
CLUSTER=$(kubectl config view --kubeconfig /work.kubeconfig -o jsonpath='{.clusters[0].name}')
kubectl config set-cluster $CLUSTER --kubeconfig /work.kubeconfig --server=https://docker:7443 --tls-server-name=localhost
for i in $(seq 1 60); do kubectl --kubeconfig /work.kubeconfig get nodes && break; sleep 5; done
kubectl --kubeconfig /work.kubeconfig wait --for=condition=Ready nodes --all --timeout=10m || { kubectl --kubeconfig /work.kubeconfig get pods -A -o wide; exit 1; }
NODE=$(docker ps -q --filter label=io.x-k8s.kind.cluster=work --filter label=io.x-k8s.kind.role=control-plane)
docker exec $NODE sh -c 'mkdir -p "/etc/containerd/certs.d/`+s.reg+`" && printf "[host.\"http://`+s.reg+`\"]\n  capabilities = [\"pull\", \"resolve\"]\n" > "/etc/containerd/certs.d/`+s.reg+`/hosts.toml" && crictl pull `+s.reg+`/probe:1'
kubectl wait cluster/work --for=condition=Available --timeout=10m || kubectl get cluster work -o yaml
`))
	s.workKC = s.exec(ctx, t, `CLUSTER=$(kubectl config view --kubeconfig /work.kubeconfig -o jsonpath='{.clusters[0].name}')
kubectl config set-cluster $CLUSTER --kubeconfig /work.kubeconfig --server=https://docker:7443 --tls-server-name=localhost >/dev/null
cat /work.kubeconfig`)
}

func (s *spike) tunnels(ctx context.Context) {
	step("host tunnels")
	tun, err := s.c.Host().Tunnel(s.dind, dagger.HostTunnelOpts{Ports: []dagger.PortForward{{Backend: 6443}, {Backend: 7443}}}).Start(ctx)
	must(err)
	ports, err := tun.Ports(ctx)
	must(err)
	var frontends []int
	for _, p := range ports {
		n, err := p.Port(ctx)
		must(err)
		d, _ := p.Description(ctx)
		fmt.Printf("tunnel port %d (%s)\n", n, d)
		frontends = append(frontends, n)
	}
	for i, kc := range []string{s.mgmtKC, s.workKC} {
		name := []string{"mgmt", "work"}[i]
		cfg, err := clientcmd.Load([]byte(kc))
		must(err)
		for _, cl := range cfg.Clusters {
			cl.Server = fmt.Sprintf("https://localhost:%d", frontends[i])
			cl.TLSServerName = ""
		}
		rest, err := clientcmd.NewDefaultClientConfig(*cfg, nil).ClientConfig()
		must(err)
		cs, err := kubernetes.NewForConfig(rest)
		must(err)
		nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		must(err)
		for _, n := range nodes.Items {
			fmt.Printf("host -> %s: node %s ready=%v\n", name, n.Name, ready(n))
		}
	}
}

func ready(n corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (s *spike) exec(ctx context.Context, ctr *dagger.Container, script string) string {
	ctr = ctr.WithExec([]string{"sh", "-c", script}, dagger.ContainerWithExecOpts{Expect: dagger.ReturnTypeAny})
	code, err := ctr.ExitCode(ctx)
	must(err)
	out, err := ctr.Stdout(ctx)
	must(err)
	errOut, err := ctr.Stderr(ctx)
	must(err)
	fmt.Fprintln(os.Stderr, errOut)
	if code != 0 {
		fmt.Println(out)
		s.diagnose(ctx)
		must(fmt.Errorf("exit %d", code))
	}
	return out
}

func (s *spike) diagnose(ctx context.Context) {
	if s.tools == nil {
		return
	}
	step("DIAGNOSTICS")
	out, _ := s.tools.WithEnvVariable("NONCE", nonce()).WithExec([]string{"sh", "-c", `set -x
docker ps -a
kubectl get pods -A -o wide
kubectl get cluster,kcp,machines,dockermachines -A -o wide
kubectl -n capd-system logs deploy/capd-controller-manager --tail=50
docker exec mgmt-control-plane journalctl -u kubelet --no-pager | tail -30`}, dagger.ContainerWithExecOpts{Expect: dagger.ReturnTypeAny}).CombinedOutput(ctx)
	fmt.Println(out)
}

func fqdn(ctx context.Context, svc *dagger.Service, domain string) string {
	h, err := svc.Hostname(ctx)
	must(err)
	return h + "." + domain
}

func hostInfo() string {
	read := func(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return fmt.Sprintf("cgroup2=%v inotify.max_user_instances=%s inotify.max_user_watches=%s nproc-ish=%s",
		err == nil, read("/proc/sys/fs/inotify/max_user_instances"), read("/proc/sys/fs/inotify/max_user_watches"), read("/sys/fs/cgroup/cpu.max"))
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func nonce() string { return time.Now().Format(time.RFC3339Nano) }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
