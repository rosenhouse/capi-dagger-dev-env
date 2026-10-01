// Package infra runs the Docker daemon and Kind clusters inside a Dagger session.
package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
)

const (
	KindVersion        = "v0.33.0"
	KubernetesVersion  = "v1.37.0"
	ClusterctlVersion  = "v1.14.2"
	MgmtAPIPort        = 6443
	WorkloadAPIPort    = 7443
	ContainerdCertsDir = "/etc/devenv/certs.d"

	// Digests avoid a registry round trip, and its rate limit, when the image is cached.
	dindImage      = "docker:29-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0"
	dockerCLIImage = "docker:29-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca"
	socatImage     = "alpine/socat:1.8.0.3@sha256:beb4a68d9e4fe6b0f21ea774a0fde6c31f580dde6368939ed70100c5385b015e"
	kindNodeImage  = "kindest/node:" + KubernetesVersion + "@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
)

// tools lists the binaries in the tools container, by URL and per-architecture checksum.
var tools = map[string]struct {
	url       string
	checksums map[string]string
}{
	"kind": {"https://github.com/kubernetes-sigs/kind/releases/download/" + KindVersion + "/kind-linux-%s", map[string]string{
		"amd64": "sha256:aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d",
		"arm64": "sha256:20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2",
	}},
	"kubectl": {"https://dl.k8s.io/release/" + KubernetesVersion + "/bin/linux/%s/kubectl", map[string]string{
		"amd64": "sha256:6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f",
		"arm64": "sha256:922df28df248cc00a9e025f947704f1d1482de64ece54cfe57e61f19eaf1eef3",
	}},
	"clusterctl": {"https://github.com/kubernetes-sigs/cluster-api/releases/download/" + ClusterctlVersion + "/clusterctl-linux-%s", map[string]string{
		"amd64": "sha256:01122674fd3c47a33206ab1b8b81d437afbcf5dd25d126535564f24a2cdf676e",
		"arm64": "sha256:83976008aa9ddb81dab01443c646aaa125e4993e17bf24e790e29779f712d79d",
	}},
}

// The node mounts the Docker socket for CAPD, and containerd config for the session registry and mirrors.
var mgmtKindConfig = fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: %d
nodes:
- role: control-plane
  extraMounts:
  - hostPath: /var/run/docker.sock
    containerPath: /var/run/docker.sock
  - hostPath: %s
    containerPath: /etc/containerd/certs.d
`, MgmtAPIPort, ContainerdCertsDir)

// Infra holds the long-lived services of one environment's Dagger session.
type Infra struct {
	c     *dagger.Client
	dind  *dagger.Service
	tools *dagger.Container
}

var session = time.Now().Format(time.RFC3339Nano)

// InSession keys a container's execs to this session. Execs that act on the session's services,
// such as its daemon or registries, must not reuse results cached by an earlier session.
func InSession(ctr *dagger.Container) *dagger.Container {
	return ctr.WithEnvVariable("DEVENV_SESSION", session)
}

// Preflight fails if the engine's host cannot run Kind.
func Preflight(ctx context.Context, c *dagger.Client) error {
	magic, err := c.Container().From(dindImage).WithExec([]string{"stat", "-fc", "%t", "/sys/fs/cgroup"}).Stdout(ctx)
	if err != nil {
		return err
	}
	return requireCgroupV2(magic)
}

// requireCgroupV2 checks the filesystem magic number of /sys/fs/cgroup, which BusyBox and GNU stat both print.
func requireCgroupV2(magic string) error {
	if magic = strings.TrimSpace(magic); magic != "63677270" {
		return fmt.Errorf("the Dagger engine's host does not mount cgroup2 at /sys/fs/cgroup (filesystem magic 0x%s); Kind inside Dagger needs cgroup v2", magic)
	}
	return nil
}

// Start starts the environment's Docker daemon and removes containers and volumes left by an earlier session.
// The daemon and its nodes pull from registryHost over plain HTTP, and from upstream registries through mirrors.
func Start(ctx context.Context, c *dagger.Client, envID, registryHost string, mirrors Mirrors) (*Infra, error) {
	platform, err := c.DefaultPlatform(ctx)
	if err != nil {
		return nil, err
	}
	arch := strings.TrimPrefix(string(platform), "linux/")

	daemon := c.Container().From(dindImage).
		WithEnvVariable("DOCKER_TLS_CERTDIR", "").
		WithNewFile(ContainerdCertsDir+"/"+registryHost+"/hosts.toml", hostsTOML("http://"+registryHost, registryHost))
	for name, mirror := range mirrors {
		daemon = daemon.WithNewFile(ContainerdCertsDir+"/"+name+"/hosts.toml", hostsTOML(Upstreams[name], mirror.Host))
	}
	dind, err := daemon.
		WithMountedCache("/var/lib/docker", c.CacheVolume("devenv-"+envID+"-docker"),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		WithExposedPort(2375).
		AsService(dagger.ContainerAsServiceOpts{UseEntrypoint: true, InsecureRootCapabilities: true, Args: []string{
			"--tls=false",
			"--registry-mirror=http://" + mirrors["docker.io"].Host,
		}}).
		Start(ctx)
	if err != nil {
		return nil, err
	}

	toolbox := c.Container().From(dockerCLIImage)
	for name, t := range tools {
		bin := c.HTTP(fmt.Sprintf(t.url, arch), dagger.HTTPOpts{Checksum: t.checksums[arch]})
		toolbox = toolbox.WithFile("/usr/local/bin/"+name, bin, dagger.ContainerWithFileOpts{Permissions: 0o755})
	}
	i := &Infra{
		c:    c,
		dind: dind,
		tools: toolbox.
			WithServiceBinding("docker", dind).
			WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
			With(InSession),
	}
	_, err = i.Run(ctx, nil, `docker rm -fv $(docker ps -aq) 2>/dev/null; docker network prune -f && docker volume prune -af`)
	return i, err
}

// CreateManagementCluster creates the Kind management cluster and returns its kubeconfig.
// Later Run calls use the cluster as kubectl's default.
func (i *Infra) CreateManagementCluster(ctx context.Context) ([]byte, error) {
	out, err := i.Run(ctx, nil, fmt.Sprintf("kind create cluster --name mgmt --image %s --config - <<'EOF'\n%sEOF\nkind get kubeconfig --name mgmt",
		kindNodeImage, mgmtKindConfig))
	if err != nil {
		return nil, err
	}
	inSession, err := kube.WithServer([]byte(out), fmt.Sprintf("https://docker:%d", MgmtAPIPort), "localhost")
	if err != nil {
		return nil, err
	}
	i.tools = i.tools.WithNewFile("/root/.kube/config", string(inSession))
	return []byte(out), nil
}

// Tunnel forwards a random host port to a DinD service port and returns the host port.
func (i *Infra) Tunnel(ctx context.Context, port int) (int, error) {
	tunnel, err := i.c.Host().Tunnel(i.dind, dagger.HostTunnelOpts{Ports: []dagger.PortForward{{Backend: port}}}).Start(ctx)
	if err != nil {
		return 0, err
	}
	ports, err := tunnel.Ports(ctx)
	if err != nil {
		return 0, err
	}
	return ports[0].Port(ctx)
}

// ForwardWorkloadAPI publishes a CAPD cluster's API server on WorkloadAPIPort of the Docker daemon.
// CAPD's load balancer publishes it on a random port, so a forwarder on the kind network gives it a fixed one.
func (i *Infra) ForwardWorkloadAPI(ctx context.Context, cluster string) error {
	_, err := i.Run(ctx, nil, fmt.Sprintf(
		"docker rm -f %[1]s-api-forward 2>/dev/null; docker run -d --name %[1]s-api-forward --network kind -p %[2]d:6443 %[3]s TCP-LISTEN:6443,fork,reuseaddr TCP:%[1]s-lb:6443",
		cluster, WorkloadAPIPort, socatImage))
	return err
}

// ExportLogs writes logs of every Kind and CAPD cluster, and the CAPI and package resources, to dir on the host.
func (i *Infra) ExportLogs(ctx context.Context, dir string) error {
	logs := i.tools.With(InSession).WithExec([]string{"sh", "-c", `mkdir -p /logs
for cluster in $(kind get clusters); do kind export logs /logs/$cluster --name $cluster; done
kubectl get clusters,machines,packageinstalls,apps -A -o yaml > /logs/resources.yaml
true`}).Directory("/logs")
	_, err := logs.Export(ctx, dir)
	return err
}

// Run runs script in the tools container, which has docker, kind, kubectl and clusterctl.
// with, if set, adds files or environment for this run.
func (i *Infra) Run(ctx context.Context, with dagger.WithContainerFunc, script string) (string, error) {
	ctr := i.tools
	if with != nil {
		ctr = ctr.With(with)
	}
	ctr = ctr.WithExec([]string{"sh", "-c", script}, dagger.ContainerWithExecOpts{Expect: dagger.ReturnTypeAny})
	code, err := ctr.ExitCode(ctx)
	if err != nil {
		return "", err
	}
	stdout, err := ctr.Stdout(ctx)
	if err != nil {
		return "", err
	}
	if code != 0 {
		stderr, _ := ctr.Stderr(ctx)
		return "", fmt.Errorf("%s: exit %d\n%s", strings.SplitN(script, "\n", 2)[0], code, tail(stderr, 20))
	}
	return stdout, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Purge deletes an environment's Docker data. It lives in a cache volume, which the Dagger API cannot delete.
func Purge(ctx context.Context, c *dagger.Client, envID string) error {
	_, err := c.Container().From(dindImage).
		With(InSession).
		WithMountedCache("/var/lib/docker", c.CacheVolume("devenv-"+envID+"-docker"),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		WithExec([]string{"sh", "-c", "rm -rf /var/lib/docker/* /var/lib/docker/.[!.]*"}).
		Sync(ctx)
	return err
}
