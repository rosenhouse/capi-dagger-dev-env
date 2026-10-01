// Package platform installs kapp-controller, Cluster API and CAPD on the management cluster,
// and creates the workload cluster.
package platform

import (
	"context"
	"fmt"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/fetch"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
)

const (
	KappControllerVersion = "v0.60.9"
	CAPIVersion           = infra.ClusterctlVersion
	CertManagerVersion    = "v1.21.1"

	kappControllerManifest = "/root/kapp-controller.yaml"
	// repo is clusterctl's local repository in the guest.
	repo = "/repo"
)

// capiReleaseChecksums pin the CAPI release assets that devenv reads.
var capiReleaseChecksums = map[string]string{
	"core-components.yaml":                       "b2fff42cb5e35440ed963a463c5ab128004481b5a4336ea0005812be2ff7e2a8",
	"bootstrap-components.yaml":                  "2a2d24f83244a6dae60e35d9e72e93c6ac3c209eedc467184737bb8eecfd63bb",
	"control-plane-components.yaml":              "7aa827b43eee898d8597bb39b7db5cf755c872c7a4da68b05cc3a9f35c459c93",
	"infrastructure-components-development.yaml": "f6aef43bf70b76a38b1628cd4349d291159fbfd8082dbe9c2ec13d04487b5867",
	"cluster-template-development.yaml":          "7dd5249f22a4ce0fbe667d1c764dd5919775f81847bceb2aa16fc5acb138b99b",
	"clusterclass-quick-start.yaml":              "85d414afc4af00e4ff4472cec46ab49a6aa85cb839a771b44ab0a6c7f6c8e24e",
	"metadata.yaml":                              "c470906f551ac3e3e9aedc9a3e733b5a5994428693df43e95f65ea67c035f9fe",
}

// providers are the CAPI release assets in clusterctl's local repository, by provider directory.
var providers = []struct {
	dir   string
	files []string
}{
	{"cluster-api", []string{"core-components.yaml"}},
	{"bootstrap-kubeadm", []string{"bootstrap-components.yaml"}},
	{"control-plane-kubeadm", []string{"control-plane-components.yaml"}},
	{"infrastructure-docker", []string{"infrastructure-components-development.yaml", "cluster-template-development.yaml", "clusterclass-quick-start.yaml"}},
}

func releaseFile(file string) fetch.File {
	return fetch.File{
		URL:    "https://github.com/kubernetes-sigs/cluster-api/releases/download/" + CAPIVersion + "/" + file,
		SHA256: capiReleaseChecksums[file],
	}
}

func providerPath(dir, file string) string { return repo + "/" + dir + "/" + CAPIVersion + "/" + file }

// Downloads are the release files that the platform installs from, and where they go in the guest.
func Downloads() []infra.Download {
	downloads := []infra.Download{{
		File: fetch.File{
			URL:    "https://github.com/carvel-dev/kapp-controller/releases/download/" + KappControllerVersion + "/release.yml",
			SHA256: "19a2984ba32d5992195886465d7d48dde8dab2bd9a3d9b04ff6ee1d8dea60e70",
		},
		Path: kappControllerManifest, Mode: 0o644,
	}, {
		File: fetch.File{
			URL:    "https://github.com/cert-manager/cert-manager/releases/download/" + CertManagerVersion + "/cert-manager.yaml",
			SHA256: "5f6a499b8c1857d57f560f536e0dcc830914b45c420899fe7ad0692c8624e408",
		},
		Path: repo + "/cert-manager/" + CertManagerVersion + "/cert-manager.yaml", Mode: 0o644,
	}}
	for _, p := range providers {
		for _, file := range append(p.files, "metadata.yaml") {
			downloads = append(downloads, infra.Download{File: releaseFile(file), Path: providerPath(p.dir, file), Mode: 0o644})
		}
	}
	return downloads
}

func InstallKappController(ctx context.Context, vm *infra.VM) error {
	return vm.Run(ctx, "kubectl apply --server-side -f "+kappControllerManifest+
		" >/dev/null\nkubectl -n kapp-controller rollout status deployment/kapp-controller --timeout=5m")
}

// InstallClusterAPI installs CAPI core, the kubeadm providers and CAPD, and waits for them.
// clusterctl reads a local file repository and skips its version check, so it makes no GitHub API calls.
func InstallClusterAPI(ctx context.Context, vm *infra.VM) error {
	if err := vm.WriteFile(ctx, repo+"/clusterctl.yaml", []byte(clusterctlConfig)); err != nil {
		return err
	}
	return vm.Run(ctx, fmt.Sprintf("CLUSTER_TOPOLOGY=true CLUSTERCTL_DISABLE_VERSIONCHECK=true clusterctl init --config %[2]s/clusterctl.yaml "+
		"--core cluster-api:%[1]s --bootstrap kubeadm:%[1]s --control-plane kubeadm:%[1]s --infrastructure docker:%[1]s "+
		"--wait-providers --wait-provider-timeout 600", CAPIVersion, repo))
}

// Apply server-side applies manifests to the management cluster.
func Apply(ctx context.Context, vm *infra.VM, manifests []byte) error {
	return vm.Pipe(ctx, "kubectl apply --server-side -f - >/dev/null", manifests)
}

// Reapply applies manifests again, taking ownership of every field they set.
// Without forcing, reapplying a Package conflicts with kubectl's own earlier apply, as kapp-controller's aggregated Package API records it.
func Reapply(ctx context.Context, vm *infra.VM, manifests []byte) error {
	return vm.Pipe(ctx, "kubectl apply --server-side --force-conflicts -f - >/dev/null", manifests)
}

var clusterctlConfig = fmt.Sprintf(`providers:
- name: cluster-api
  type: CoreProvider
  url: %[3]s
- name: kubeadm
  type: BootstrapProvider
  url: %[4]s
- name: kubeadm
  type: ControlPlaneProvider
  url: %[5]s
- name: docker
  type: InfrastructureProvider
  url: %[6]s
cert-manager:
  url: %[1]s/cert-manager/%[2]s/cert-manager.yaml
  version: %[2]s
`, repo, CertManagerVersion,
	providerPath("cluster-api", "core-components.yaml"),
	providerPath("bootstrap-kubeadm", "bootstrap-components.yaml"),
	providerPath("control-plane-kubeadm", "control-plane-components.yaml"),
	providerPath("infrastructure-docker", "infrastructure-components-development.yaml"))
