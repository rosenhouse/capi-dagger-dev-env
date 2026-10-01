// Package infra runs an environment's smolvm VM: its Docker daemon, session registry and Kind clusters.
package infra

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/fetch"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

const (
	KindVersion        = "v0.33.0"
	KubernetesVersion  = "v1.37.0"
	ClusterctlVersion  = "v1.14.2"
	DockerVersion      = "29.8.2"
	MgmtAPIPort        = 6443
	WorkloadAPIPort    = 7443
	RegistryPort       = 5000
	ContainerdCertsDir = "/etc/devenv/certs.d"
	// Registry is the session registry's address on the kind network, where the clusters pull first-party images.
	// It is a private IP, so clients use plain HTTP.
	Registry   = registryIP + ":5000"
	registryIP = "172.31.255.254"

	// Digests avoid a registry round trip, and its rate limit, when the image is cached.
	registryImage = "registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"
	socatImage    = "alpine/socat:1.8.0.3@sha256:beb4a68d9e4fe6b0f21ea774a0fde6c31f580dde6368939ed70100c5385b015e"
	kindNodeImage = "kindest/node:" + KubernetesVersion + "@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
)

// machineConfig sizes each environment's VM. Two fit on a 16 GB host.
var machineConfig = smolvm.MachineConfig{CPUs: 4, MemoryMiB: 5120, StorageGiB: 40, OverlayGiB: 10}

// VM is an environment's machine.
type VM struct {
	CLI  smolvm.CLI
	Name string
	// Log receives the output of guest commands.
	Log io.Writer
}

// Create creates and starts the VM, publishing the guest's API servers and registry on the given host ports.
func (v *VM) Create(ctx context.Context, ports state.Ports) error {
	cfg := machineConfig
	cfg.Ports = []smolvm.Port{
		{Host: ports.MgmtAPI, Guest: MgmtAPIPort},
		{Host: ports.WorkloadAPI, Guest: WorkloadAPIPort},
		{Host: ports.Registry, Guest: RegistryPort},
	}
	if err := v.CLI.Create(ctx, v.Name, cfg); err != nil {
		return err
	}
	return v.CLI.Start(ctx, v.Name, smolvm.StartOptions{})
}

func (v *VM) Delete(ctx context.Context) error {
	return v.CLI.Delete(ctx, v.Name, smolvm.DeleteOptions{})
}

var guestEnv = []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}

// Run runs script in the guest.
func (v *VM) Run(ctx context.Context, script string) error {
	_, err := v.CLI.Run(ctx, v.Name, script, smolvm.ExecOptions{Env: guestEnv, Stdout: v.Log, Stderr: v.Log})
	return err
}

// Output runs script in the guest and returns its stdout, which it leaves out of the log.
func (v *VM) Output(ctx context.Context, script string) (string, error) {
	return v.CLI.Run(ctx, v.Name, script, smolvm.ExecOptions{Env: guestEnv, Stderr: v.Log})
}

// Pipe runs script in the guest with stdin.
func (v *VM) Pipe(ctx context.Context, script string, stdin []byte) error {
	_, err := v.CLI.Run(ctx, v.Name, script, smolvm.ExecOptions{Env: guestEnv, Stdin: bytes.NewReader(stdin), Stdout: v.Log, Stderr: v.Log})
	return err
}

// WriteFile writes content to path in the guest.
func (v *VM) WriteFile(ctx context.Context, path string, content []byte) error {
	return v.Pipe(ctx, fmt.Sprintf("mkdir -p \"$(dirname %[1]s)\"\ncat >%[1]s", path), content)
}

// Download is a pinned file and where it goes in the guest.
type Download struct {
	fetch.File
	Path string
	Mode fs.FileMode
}

// Copy puts downloads into the guest, from the cache or else the network.
func (v *VM) Copy(ctx context.Context, cache fetch.Cache, downloads []Download) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for _, d := range downloads {
		g.Go(func() error {
			path, err := cache.Get(ctx, d.File)
			if err != nil {
				return err
			}
			return v.CLI.CopyIn(ctx, v.Name, path, d.Path, d.Mode)
		})
	}
	return g.Wait()
}

const alpine = "https://dl-cdn.alpinelinux.org/alpine/v3.19/main/"

// tools are the guest's tools. Their URLs take the architecture as Go names it (%[1]s), or as Docker and Alpine do (%[2]s).
// The guest runs Alpine 3.19, which lacks iptables for Docker.
var tools = []struct {
	url, path string
	mode      fs.FileMode
	sha256    map[string]string
}{
	{"https://download.docker.com/linux/static/stable/%[2]s/docker-" + DockerVersion + ".tgz", "/opt/devenv/docker.tgz", 0o644, map[string]string{
		"amd64": "995d1ef289677f74fd58d8d2c35727b6a4ee389c69db8638a3e42d0487aa5b0f",
		"arm64": "76a624e4a8e5da654d1150e808175125efb5a6f1b6aa1cbd9caee18f51047a50",
	}},
	{alpine + "%[2]s/iptables-1.8.10-r3.apk", "/opt/devenv/apk/iptables.apk", 0o644, map[string]string{
		"amd64": "12705a169233c46d4e90307b0fbd17ea476ca836853bfe8edcb9742fdd9789ee",
		"arm64": "31ab6343f1f3d0fbbf290c4dcf0430b2d08e8073e516e13530dfab25b097d467",
	}},
	{alpine + "%[2]s/libmnl-1.0.5-r2.apk", "/opt/devenv/apk/libmnl.apk", 0o644, map[string]string{
		"amd64": "322ce883205831dce94e90b6c3dee4fccb2099b8fa46ecc971dead14ce22deb4",
		"arm64": "d15e6313880bdd14959f42c1556b4a810ef4894992ae9f73b148126f0cc6021d",
	}},
	{alpine + "%[2]s/libnftnl-1.2.6-r0.apk", "/opt/devenv/apk/libnftnl.apk", 0o644, map[string]string{
		"amd64": "c533fa38066450ae9c0c3f794e7314514280e4cd34d49ec199028599b6352d49",
		"arm64": "ec1c2b02869fc65bcf7a1105e3a7ac5df1bd9a8bd8b399cb6cf650dd3112c021",
	}},
	{alpine + "%[2]s/libxtables-1.8.10-r3.apk", "/opt/devenv/apk/libxtables.apk", 0o644, map[string]string{
		"amd64": "9950f4f49c0086e48d0e437cbe9b5badcaad747ff7689bddd10f20c02485f764",
		"arm64": "f0accefde240ece6722479b46cb014d7d2f745af7d796e8eec3ced53571e1088",
	}},
	{"https://github.com/kubernetes-sigs/kind/releases/download/" + KindVersion + "/kind-linux-%[1]s", "/usr/local/bin/kind", 0o755, map[string]string{
		"amd64": "aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d",
		"arm64": "20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2",
	}},
	{"https://dl.k8s.io/release/" + KubernetesVersion + "/bin/linux/%[1]s/kubectl", "/usr/local/bin/kubectl", 0o755, map[string]string{
		"amd64": "6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f",
		"arm64": "922df28df248cc00a9e025f947704f1d1482de64ece54cfe57e61f19eaf1eef3",
	}},
	{"https://github.com/kubernetes-sigs/cluster-api/releases/download/" + ClusterctlVersion + "/clusterctl-linux-%[1]s", "/usr/local/bin/clusterctl", 0o755, map[string]string{
		"amd64": "01122674fd3c47a33206ab1b8b81d437afbcf5dd25d126535564f24a2cdf676e",
		"arm64": "83976008aa9ddb81dab01443c646aaa125e4993e17bf24e790e29779f712d79d",
	}},
}

var linuxArch = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}

// Downloads are the guest's tools for a guest of architecture goarch.
func Downloads(goarch string) ([]Download, error) {
	arch, ok := linuxArch[goarch]
	if !ok {
		return nil, fmt.Errorf("devenv runs on amd64 and arm64, not %s", goarch)
	}
	var downloads []Download
	for _, t := range tools {
		downloads = append(downloads, Download{
			File: fetch.File{URL: fmt.Sprintf(t.url, goarch, arch), SHA256: t.sha256[goarch]},
			Path: t.path,
			Mode: t.mode,
		})
	}
	return downloads, nil
}

// Install installs the tools that Copy put in the guest. Each package's sha256 is pinned, so apk need not check signatures.
func (v *VM) Install(ctx context.Context) error {
	return v.Run(ctx, `tar -xzf /opt/devenv/docker.tgz -C /usr/local/bin --strip-components=1
apk add --quiet --no-network --allow-untrusted /opt/devenv/apk/*.apk`)
}

// StartDocker starts dockerd with its data on the VM's storage disk, because overlay2 cannot nest on the root overlay.
// Without --storage-driver, a fresh Docker 29 would use the containerd image store instead of overlay2.
func (v *VM) StartDocker(ctx context.Context) error {
	if err := v.Run(ctx, `mkdir -p /storage/docker /var/lib/docker /storage/containerd /var/lib/containerd /lib/modules `+ContainerdCertsDir+`
mountpoint -q /var/lib/docker || mount --bind /storage/docker /var/lib/docker
mountpoint -q /var/lib/containerd || mount --bind /storage/containerd /var/lib/containerd
mount --make-rshared /
sysctl -w fs.inotify.max_user_instances=8192 >/dev/null
sysctl -w fs.inotify.max_user_watches=1048576 >/dev/null
rm -f /var/run/docker.pid /var/run/docker/containerd/containerd.pid`); err != nil {
		return err
	}
	if _, err := v.CLI.Spawn(ctx, v.Name, []string{"sh", "-c", "exec dockerd --storage-driver=overlay2 >>/var/log/dockerd.log 2>&1"}, guestEnv); err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: "dockerd answers", Timeout: time.Minute, Interval: time.Second,
		Check: func(ctx context.Context) error { return v.Run(ctx, "docker info >/dev/null") },
	})
}

// StartRegistry creates the kind network and runs the session registry on it at Registry, published on RegistryPort.
// The network's dynamic range leaves out Registry's address. Containerd in every node pulls from it over plain HTTP.
func (v *VM) StartRegistry(ctx context.Context) error {
	return v.Run(ctx, fmt.Sprintf(`docker network create -d bridge -o com.docker.network.bridge.enable_ip_masquerade=true \
  -o com.docker.network.driver.mtu=1500 --subnet 172.31.0.0/16 --ip-range 172.31.0.0/17 kind >/dev/null
docker run -d --name devenv-registry --restart=always --network kind --ip %[1]s -p %[2]d:5000 %[3]s >/dev/null
mkdir -p %[4]s/%[5]s
cat >%[4]s/%[5]s/hosts.toml <<'EOF'
%[6]sEOF`, registryIP, RegistryPort, registryImage, ContainerdCertsDir, Registry, hostsTOML("http://"+Registry, Registry)))
}

// hostsTOML renders containerd registry config that tries mirror, then falls back to server.
func hostsTOML(server, mirror string) string {
	return fmt.Sprintf("server = %q\n\n[host.%q]\n  capabilities = [\"pull\", \"resolve\"]\n", server, "http://"+mirror)
}

// The node mounts the Docker socket for CAPD, and containerd config for the session registry.
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

// CreateManagementCluster creates the Kind management cluster and returns its kubeconfig.
// Later guest commands use the cluster as kubectl's default.
func (v *VM) CreateManagementCluster(ctx context.Context) ([]byte, error) {
	if err := v.Run(ctx, fmt.Sprintf("kind create cluster --name mgmt --image %s --config - <<'EOF'\n%sEOF", kindNodeImage, mgmtKindConfig)); err != nil {
		return nil, err
	}
	out, err := v.Output(ctx, "kind get kubeconfig --name mgmt")
	return []byte(out), err
}

// ForwardWorkloadAPI publishes a CAPD cluster's API server on WorkloadAPIPort.
// CAPD's load balancer publishes it on a random port, so a forwarder on the kind network gives it a fixed one.
func (v *VM) ForwardWorkloadAPI(ctx context.Context, cluster string) error {
	return v.Run(ctx, fmt.Sprintf(
		"docker rm -f %[1]s-api-forward >/dev/null 2>&1 || true\ndocker run -d --name %[1]s-api-forward --network kind -p %[2]d:6443 %[3]s TCP-LISTEN:6443,fork,reuseaddr TCP:%[1]s-lb:6443 >/dev/null",
		cluster, WorkloadAPIPort, socatImage))
}
