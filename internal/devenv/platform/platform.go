// Package platform installs kapp-controller, Cluster API and CAPD on the management cluster.
package platform

import (
	"context"
	"fmt"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
)

const (
	KappControllerVersion = "v0.60.9"
	CAPIVersion           = infra.ClusterctlVersion
	CertManagerVersion    = "v1.21.1"
)

// capiReleaseChecksums pin the CAPI release assets that devenv reads.
var capiReleaseChecksums = map[string]string{
	"core-components.yaml":                       "sha256:b2fff42cb5e35440ed963a463c5ab128004481b5a4336ea0005812be2ff7e2a8",
	"bootstrap-components.yaml":                  "sha256:2a2d24f83244a6dae60e35d9e72e93c6ac3c209eedc467184737bb8eecfd63bb",
	"control-plane-components.yaml":              "sha256:7aa827b43eee898d8597bb39b7db5cf755c872c7a4da68b05cc3a9f35c459c93",
	"infrastructure-components-development.yaml": "sha256:f6aef43bf70b76a38b1628cd4349d291159fbfd8082dbe9c2ec13d04487b5867",
	"metadata.yaml":                              "sha256:c470906f551ac3e3e9aedc9a3e733b5a5994428693df43e95f65ea67c035f9fe",
}

func InstallKappController(ctx context.Context, c *dagger.Client, inf *infra.Infra) error {
	release := c.HTTP("https://github.com/carvel-dev/kapp-controller/releases/download/"+KappControllerVersion+"/release.yml",
		dagger.HTTPOpts{Checksum: "sha256:19a2984ba32d5992195886465d7d48dde8dab2bd9a3d9b04ff6ee1d8dea60e70"})
	_, err := inf.Run(ctx, func(t *dagger.Container) *dagger.Container { return t.WithFile("/kapp-controller.yaml", release) },
		"kubectl apply --server-side -f /kapp-controller.yaml >/dev/null && kubectl -n kapp-controller rollout status deployment/kapp-controller --timeout=5m")
	return err
}

// InstallClusterAPI installs CAPI core, the kubeadm providers and CAPD, and waits for them.
// clusterctl reads a local file repository and skips its version check, so it makes no GitHub API calls.
func InstallClusterAPI(ctx context.Context, c *dagger.Client, inf *infra.Infra) error {
	_, err := inf.Run(ctx, func(t *dagger.Container) *dagger.Container {
		return t.WithDirectory("/repo", clusterctlRepository(c)).
			WithEnvVariable("CLUSTER_TOPOLOGY", "true").
			WithEnvVariable("CLUSTERCTL_DISABLE_VERSIONCHECK", "true")
	}, fmt.Sprintf("clusterctl init --config /repo/clusterctl.yaml --core cluster-api:%[1]s --bootstrap kubeadm:%[1]s --control-plane kubeadm:%[1]s --infrastructure docker:%[1]s --wait-providers --wait-provider-timeout 600", CAPIVersion))
	return err
}

// Apply server-side applies manifests to the management cluster.
func Apply(ctx context.Context, inf *infra.Infra, manifests []byte) error {
	_, err := inf.Run(ctx, func(t *dagger.Container) *dagger.Container {
		return t.WithNewFile("/manifests.yaml", string(manifests))
	},
		"kubectl apply --server-side -f /manifests.yaml")
	return err
}

// clusterctlFiles are the CAPI release assets in clusterctl's local repository, by provider directory.
var clusterctlFiles = []struct{ dir, file string }{
	{"cluster-api", "core-components.yaml"},
	{"bootstrap-kubeadm", "bootstrap-components.yaml"},
	{"control-plane-kubeadm", "control-plane-components.yaml"},
	{"infrastructure-docker", "infrastructure-components-development.yaml"},
}

func clusterctlRepository(c *dagger.Client) *dagger.Directory {
	release := func(file string) *dagger.File {
		return c.HTTP("https://github.com/kubernetes-sigs/cluster-api/releases/download/"+CAPIVersion+"/"+file,
			dagger.HTTPOpts{Checksum: capiReleaseChecksums[file]})
	}
	metadata := release("metadata.yaml")
	repo := c.Directory()
	for _, p := range clusterctlFiles {
		dir := p.dir + "/" + CAPIVersion + "/"
		repo = repo.WithFile(dir+p.file, release(p.file)).WithFile(dir+"metadata.yaml", metadata)
	}
	return repo.
		WithFile("cert-manager/"+CertManagerVersion+"/cert-manager.yaml",
			c.HTTP("https://github.com/cert-manager/cert-manager/releases/download/"+CertManagerVersion+"/cert-manager.yaml",
				dagger.HTTPOpts{Checksum: "sha256:5f6a499b8c1857d57f560f536e0dcc830914b45c420899fe7ad0692c8624e408"})).
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
`, CAPIVersion, CertManagerVersion))
}
