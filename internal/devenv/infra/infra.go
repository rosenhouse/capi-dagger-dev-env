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
	MgmtAPIPort       = 6443

	// Digests avoid a registry round trip, and its rate limit, when the image is cached.
	dindImage      = "docker:29-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0"
	dockerCLIImage = "docker:29-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca"
	kindNodeImage  = "kindest/node:" + KubernetesVersion + "@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
)

var kindChecksums = map[string]string{
	"amd64": "sha256:aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d",
	"arm64": "sha256:20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2",
}

var mgmtKindConfig = fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: %d
`, MgmtAPIPort)

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
func Start(ctx context.Context, c *dagger.Client, envID string) (*Infra, error) {
	platform, err := c.DefaultPlatform(ctx)
	if err != nil {
		return nil, err
	}
	arch := strings.TrimPrefix(string(platform), "linux/")

	dind, err := c.Container().From(dindImage).
		WithEnvVariable("DOCKER_TLS_CERTDIR", "").
		WithMountedCache("/var/lib/docker", c.CacheVolume("devenv-"+envID+"-docker"),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		WithExposedPort(2375).
		AsService(dagger.ContainerAsServiceOpts{UseEntrypoint: true, InsecureRootCapabilities: true, Args: []string{"--tls=false"}}).
		Start(ctx)
	if err != nil {
		return nil, err
	}

	kind := c.HTTP("https://github.com/kubernetes-sigs/kind/releases/download/"+KindVersion+"/kind-linux-"+arch,
		dagger.HTTPOpts{Checksum: kindChecksums[arch]})
	i := &Infra{
		c:    c,
		dind: dind,
		tools: c.Container().From(dockerCLIImage).
			WithFile("/usr/local/bin/kind", kind, dagger.ContainerWithFileOpts{Permissions: 0o755}).
			WithServiceBinding("docker", dind).
			WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
			With(InSession),
	}
	_, err = i.run(ctx, `docker rm -fv $(docker ps -aq) 2>/dev/null; docker network prune -f && docker volume prune -af`)
	return i, err
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
