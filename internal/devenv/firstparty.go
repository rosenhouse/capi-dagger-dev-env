package devenv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/bundle"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/hostbuild"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/oci"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
)

// firstParty pushes the first-party images and bundles, installs the management packages,
// and waits for addon-manager to install greeting-controller into the workload cluster.
func (e *Environment) firstParty(ctx context.Context, b build) error {
	if err := e.stage("push images and bundles", func() error { return e.push(ctx, b) }); err != nil {
		return err
	}
	if err := e.stage("management packages", func() error { return e.installPackages(ctx) }); err != nil {
		return err
	}
	return e.stage("workload packages", func() error {
		dyn, err := kube.Dynamic(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "remote PackageInstall reconciled", Timeout: 5 * time.Minute, Interval: 3 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error {
				return kube.PackageInstallReconciled(ctx, dyn, WorkloadNamespace, remotePackageInstall)
			},
		})
	})
}

const (
	packageVersion = "0.1.0"
	baseImage      = "gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3"
)

// packages lists the first-party bundles, the images each one locks, and whether it runs on the management cluster.
var packages = []struct {
	name   string
	images []string
	mgmt   bool
}{
	{"addon-manager", []string{"addon-manager"}, true},
	{"greeting-syncer", []string{"greeting-syncer"}, true},
	{"greeting-controller", []string{"greeting-controller", "hello"}, false},
}

func refName(pkg string) string { return pkg + ".demo.example.com" }

// build is the first-party images and package manifests from one snapshot of the source.
type build struct {
	images map[string]v1.Image
	config map[string]map[string][]byte
}

// build builds images from the current source, stamped with version, or with a digest of the source if version is empty.
func (e *Environment) build(ctx context.Context, version string) (build, error) {
	wd, err := os.Getwd()
	if err != nil {
		return build{}, err
	}
	root, err := hostbuild.ModuleRoot(wd)
	if err != nil {
		return build{}, err
	}
	out, err := os.MkdirTemp("", "devenv-build-")
	if err != nil {
		return build{}, err
	}
	defer os.RemoveAll(out)
	snap, err := hostbuild.Build(ctx, root, runtime.GOARCH, version, out)
	if err != nil {
		return build{}, err
	}
	base, err := oci.Base(ctx, baseImage, v1.Platform{OS: "linux", Architecture: runtime.GOARCH}, filepath.Join(e.cacheDir, "images"))
	if err != nil {
		return build{}, err
	}
	b := build{images: map[string]v1.Image{}, config: snap.Config}
	for _, name := range hostbuild.Commands {
		bin, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			return build{}, err
		}
		if b.images[name], err = oci.Image(base, name, bin); err != nil {
			return build{}, err
		}
	}
	return b, nil
}

// push pushes b through the registry's host port, and renders Packages that pull the bundles from inside the clusters.
func (e *Environment) push(ctx context.Context, b build) error {
	host := fmt.Sprintf("localhost:%d/", e.Ports.Registry)
	refs := map[string]string{}
	for name, img := range b.images {
		digest, err := oci.Push(ctx, img, host+name)
		if err != nil {
			return fmt.Errorf("push %s: %w", name, err)
		}
		refs[name] = infra.Registry + "/" + name + "@" + digest.String()
	}
	var pkgs [][]byte
	bundles := map[string]string{}
	for _, p := range packages {
		images, err := subset(refs, p.images)
		if err != nil {
			return fmt.Errorf("package %s: %w", p.name, err)
		}
		lock, err := bundle.ImagesLock(images)
		if err != nil {
			return err
		}
		config, ok := b.config[p.name]
		if !ok {
			return fmt.Errorf("package %s has no config/%s", p.name, p.name)
		}
		img, err := oci.Bundle(config, lock)
		if err != nil {
			return err
		}
		digest, err := oci.Push(ctx, img, host+"bundles/"+p.name)
		if err != nil {
			return fmt.Errorf("push bundle %s: %w", p.name, err)
		}
		ref := infra.Registry + "/bundles/" + p.name + "@" + digest.String()
		pkg, err := bundle.Package(refName(p.name), packageVersion, ref)
		if err != nil {
			return err
		}
		pkgs = append(pkgs, pkg)
		bundles[p.name] = ref
	}
	e.Packages, e.bundles = pkgs, bundles
	return nil
}

// installerManifests let kapp-controller install the management packages with cluster-admin.
const installerManifests = `apiVersion: v1
kind: Namespace
metadata:
  name: devenv
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: devenv-installer
  namespace: devenv
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: devenv-installer
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: devenv-installer
  namespace: devenv
`

func (e *Environment) installPackages(ctx context.Context) error {
	manifests := [][]byte{[]byte(installerManifests)}
	manifests = append(manifests, e.Packages...)
	for _, p := range packages {
		if !p.mgmt {
			continue
		}
		pkgi, err := bundle.PackageInstall(refName(p.name), packageVersion, "devenv", "devenv-installer")
		if err != nil {
			return err
		}
		manifests = append(manifests, pkgi)
	}
	if err := platform.Apply(ctx, e.vm, bytes.Join(manifests, []byte("---\n"))); err != nil {
		return err
	}
	dyn, err := kube.Dynamic(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: "PackageInstalls reconciled", Timeout: 5 * time.Minute, Interval: 3 * time.Second, Attempt: apiAttempt,
		Check: func(ctx context.Context) error { return kube.PackageInstallsReconciled(ctx, dyn, "devenv") },
	})
}

func subset(m map[string]string, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			return nil, fmt.Errorf("no image %q", k)
		}
		out[k] = v
	}
	return out, nil
}

// validVersion matches versions that -ldflags can stamp, or none.
var validVersion = regexp.MustCompile(`^[A-Za-z0-9._+-]*$`)

// Redeploy rebuilds images and bundles from the current source and waits for every package,
// in both clusters, to deploy its new bundle. An empty version names the build by its source.
func (e *Environment) Redeploy(ctx context.Context, version string) error {
	if !validVersion.MatchString(version) {
		return fmt.Errorf("version %q is not letters, digits and ._+-", version)
	}
	var b build
	if err := e.stage("build images and bundles", func() (err error) { b, err = e.build(ctx, version); return err }); err != nil {
		return err
	}
	if err := e.stage("push images and bundles", func() error { return e.push(ctx, b) }); err != nil {
		return err
	}
	return e.stage("redeploy packages", func() error {
		if err := platform.Reapply(ctx, e.vm, bytes.Join(e.Packages, []byte("---\n"))); err != nil {
			return err
		}
		dyn, err := kube.Dynamic(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		for _, p := range packages {
			namespace, app := "devenv", p.name
			if !p.mgmt {
				namespace, app = WorkloadNamespace, remotePackageInstall
			}
			if err := ready.Wait(ctx, ready.Gate{
				Name: fmt.Sprintf("App %s/%s deployed", namespace, app), Timeout: 5 * time.Minute, Interval: 2 * time.Second, Attempt: apiAttempt,
				Check: func(ctx context.Context) error {
					return kube.AppDeployed(ctx, dyn, namespace, app, e.bundles[p.name])
				},
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
