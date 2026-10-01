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

func InstallKappController(ctx context.Context, c *dagger.Client, inf *infra.Infra) error {
	release := c.HTTP("https://github.com/carvel-dev/kapp-controller/releases/download/" + KappControllerVersion + "/release.yml")
	_, err := inf.Run(ctx, func(t *dagger.Container) *dagger.Container { return t.WithFile("/kapp-controller.yaml", release) },
		"kubectl apply --server-side -f /kapp-controller.yaml >/dev/null && kubectl -n kapp-controller rollout status deployment/kapp-controller --timeout=5m")
	return err
}

// InstallClusterAPI installs CAPI core, the kubeadm providers and CAPD, and waits for them.
// clusterctl reads a local file repository, so it makes no GitHub API calls.
func InstallClusterAPI(ctx context.Context, c *dagger.Client, inf *infra.Infra) error {
	_, err := inf.Run(ctx, func(t *dagger.Container) *dagger.Container {
		return t.WithDirectory("/repo", clusterctlRepository(c)).
			WithEnvVariable("CLUSTER_TOPOLOGY", "true").
			WithEnvVariable("GOPROXY", "off")
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

func clusterctlRepository(c *dagger.Client) *dagger.Directory {
	release := func(file string) *dagger.File {
		return c.HTTP("https://github.com/kubernetes-sigs/cluster-api/releases/download/" + CAPIVersion + "/" + file)
	}
	metadata := release("metadata.yaml")
	repo := c.Directory()
	for _, p := range []struct{ dir, file string }{
		{"cluster-api", "core-components.yaml"},
		{"bootstrap-kubeadm", "bootstrap-components.yaml"},
		{"control-plane-kubeadm", "control-plane-components.yaml"},
		{"infrastructure-docker", "infrastructure-components-development.yaml"},
		{"infrastructure-docker", "cluster-template-development.yaml"},
		{"infrastructure-docker", "clusterclass-quick-start.yaml"},
	} {
		dir := p.dir + "/" + CAPIVersion + "/"
		repo = repo.WithFile(dir+p.file, release(p.file)).WithFile(dir+"metadata.yaml", metadata)
	}
	return repo.
		WithFile("cert-manager/"+CertManagerVersion+"/cert-manager.yaml",
			c.HTTP("https://github.com/cert-manager/cert-manager/releases/download/"+CertManagerVersion+"/cert-manager.yaml")).
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
