// Package infra runs the Docker daemon and Kind clusters inside a Dagger session.
package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dagger.io/dagger"
)

const (
	KindVersion       = "v0.33.0"
	KubernetesVersion = "v1.37.0"

	// MgmtAPIPort is where the DinD service publishes the management API server.
	MgmtAPIPort = 6443

	// Digests avoid a registry round trip, and its rate limit, when the image is cached.
	dindImage      = "docker:29-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0"
	dockerCLIImage = "docker:29-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca"
	kindNodeImage  = "kindest/node:" + KubernetesVersion + "@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
)

const mgmtKindConfig = `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: 6443
`

// Infra holds the long-lived services of one environment's Dagger session.
type Infra struct {
	c     *dagger.Client
	dind  *dagger.Service
	tools *dagger.Container
}

// Start starts the environment's Docker daemon and removes containers left by an earlier session.
func Start(ctx context.Context, c *dagger.Client, env string) (*Infra, error) {
	platform, err := c.DefaultPlatform(ctx)
	if err != nil {
		return nil, err
	}
	arch := strings.TrimPrefix(string(platform), "linux/")

	dind, err := c.Container().From(dindImage).
		WithEnvVariable("DOCKER_TLS_CERTDIR", "").
		WithMountedCache("/var/lib/docker", c.CacheVolume("devenv-"+env+"-docker"),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		WithExposedPort(2375).
		AsService(dagger.ContainerAsServiceOpts{UseEntrypoint: true, InsecureRootCapabilities: true, Args: []string{"--tls=false"}}).
		Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("start Docker daemon: %w", err)
	}

	i := &Infra{
		c:    c,
		dind: dind,
		tools: c.Container().From(dockerCLIImage).
			WithFile("/usr/local/bin/kind",
				c.HTTP("https://github.com/kubernetes-sigs/kind/releases/download/"+KindVersion+"/kind-linux-"+arch),
				dagger.ContainerWithFileOpts{Permissions: 0o755}).
			WithServiceBinding("docker", dind).
			WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
			// Each session acts on a fresh daemon, so its execs must not reuse cached results.
			WithEnvVariable("DEVENV_SESSION", time.Now().Format(time.RFC3339Nano)),
	}
	magic, err := i.run(ctx, "stat -fc %t /sys/fs/cgroup")
	if err != nil {
		return nil, err
	}
	if err := requireCgroupV2(magic); err != nil {
		return nil, err
	}
	_, err = i.run(ctx, `docker rm -f $(docker ps -aq) 2>/dev/null; docker network prune -f`)
	return i, err
}

// requireCgroupV2 checks the filesystem magic number of /sys/fs/cgroup, which BusyBox and GNU stat both print.
func requireCgroupV2(magic string) error {
	if magic = strings.TrimSpace(magic); magic != "63677270" {
		return fmt.Errorf("the Dagger engine's host does not mount cgroup2 at /sys/fs/cgroup (filesystem magic 0x%s); Kind inside Dagger needs cgroup v2", magic)
	}
	return nil
}

// CreateManagementCluster creates the Kind management cluster and returns its kubeconfig.
func (i *Infra) CreateManagementCluster(ctx context.Context) ([]byte, error) {
	out, err := i.run(ctx, fmt.Sprintf("kind create cluster --name mgmt --image %s --config - <<'EOF'\n%sEOF\nkind get kubeconfig --name mgmt",
		kindNodeImage, mgmtKindConfig))
	return []byte(out), err
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

// ExportLogs writes Kind's cluster logs to dir on the host.
func (i *Infra) ExportLogs(ctx context.Context, dir string) error {
	logs := i.tools.WithExec([]string{"sh", "-c", "kind export logs /logs --name mgmt || true"}).Directory("/logs")
	_, err := logs.Export(ctx, dir)
	return err
}

func (i *Infra) run(ctx context.Context, script string) (string, error) {
	ctr := i.tools.WithExec([]string{"sh", "-c", script}, dagger.ContainerWithExecOpts{Expect: dagger.ReturnTypeAny})
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
