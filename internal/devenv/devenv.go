// Package devenv brings up a development environment in one Dagger session.
package devenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"dagger.io/dagger"
	"golang.org/x/sync/errgroup"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/build"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/bundle"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

type Options struct {
	Name     string
	StateDir string
	// Verbose streams Dagger logs to stderr as well as to the log file.
	Verbose bool
	// Progress receives one line per stage.
	Progress io.Writer
}

// Environment is a running environment. It lives until Close.
type Environment struct {
	state.Env
	MgmtKubeconfig string
	// Packages are rendered Package resources for the first-party bundles in the session registry.
	Packages [][]byte

	opts    Options
	start   time.Time
	infra   *infra.Infra
	closers []func() error
}

// Up brings up an environment and waits for its readiness gates.
func Up(ctx context.Context, o Options) (*Environment, error) {
	env, err := state.New(o.StateDir, o.Name)
	if err != nil {
		return nil, err
	}
	unlock, err := env.Lock()
	if err != nil {
		return nil, err
	}
	e := &Environment{Env: env, opts: o, start: time.Now(), closers: []func() error{func() error { unlock(); return nil }}}
	if err := e.bringUp(ctx); err != nil {
		e.ExportLogs()
		e.Close()
		return nil, fmt.Errorf("%w\nlogs: %s", err, env.Dir)
	}
	return e, nil
}

func (e *Environment) bringUp(ctx context.Context) error {
	logPath := filepath.Join(e.Dir, "dagger.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	e.closers = append([]func() error{logFile.Close}, e.closers...)
	var logs io.Writer = logFile
	if e.opts.Verbose {
		logs = io.MultiWriter(logFile, os.Stderr)
	}

	var c *dagger.Client
	if err := e.stage("connect to Dagger engine, logging to "+logPath, func() (err error) {
		c, err = dagger.Connect(ctx, dagger.WithLogOutput(logs),
			dagger.WithEnvironmentVariable("DAGGER_PROGRESS", "plain"),
			dagger.WithEnvironmentVariable("NO_COLOR", "1"))
		return err
	}); err != nil {
		return err
	}
	e.closers = append([]func() error{c.Close}, e.closers...)

	if err := e.stage("preflight", func() error { return infra.Preflight(ctx, c) }); err != nil {
		return err
	}
	var reg *infra.Registry
	if err := e.stage("registry", func() (err error) {
		reg, err = infra.StartRegistry(ctx, c)
		return err
	}); err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.managementCluster(gctx, c, reg.Host) })
	g.Go(func() error {
		return e.stage("images and bundles", func() error { return e.publishPackages(gctx, c, reg) })
	})
	if err := g.Wait(); err != nil {
		return err
	}
	if err := e.stage("management packages", func() error { return e.installPackages(ctx) }); err != nil {
		return err
	}
	return e.stage("workload cluster", func() error { return e.workloadCluster(ctx, c) })
}

const (
	workloadCluster   = "work"
	workloadNamespace = "default"
	// remotePackageInstall is the name addon-manager gives greeting-controller's PackageInstall.
	remotePackageInstall = workloadCluster + "-greeting-controller"
)

// workloadCluster creates the workload cluster and waits for addon-manager to install greeting-controller into it.
func (e *Environment) workloadCluster(ctx context.Context, c *dagger.Client) error {
	if err := platform.CreateWorkloadCluster(ctx, c, e.infra, workloadCluster, workloadNamespace); err != nil {
		return err
	}
	dyn, err := kube.Dynamic(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	if err := ready.Wait(ctx, ready.Gate{
		Name: "workload Cluster Available", Timeout: 10 * time.Minute, Interval: 5 * time.Second,
		Check: func(ctx context.Context) error {
			return kube.ClusterAvailable(ctx, dyn, workloadNamespace, workloadCluster)
		},
	}); err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: "remote PackageInstall reconciled", Timeout: 5 * time.Minute, Interval: 3 * time.Second,
		Check: func(ctx context.Context) error {
			return kube.PackageInstallReconciled(ctx, dyn, workloadNamespace, remotePackageInstall)
		},
	})
}

func (e *Environment) managementCluster(ctx context.Context, c *dagger.Client, registryHost string) error {
	if err := e.stage("docker daemon", func() (err error) {
		e.infra, err = infra.Start(ctx, c, e.ID, registryHost)
		return err
	}); err != nil {
		return err
	}
	if err := e.stage("management cluster", func() error {
		kubeconfig, err := e.infra.CreateManagementCluster(ctx)
		if err != nil {
			return err
		}
		port, err := e.infra.Tunnel(ctx, infra.MgmtAPIPort)
		if err != nil {
			return err
		}
		if e.MgmtKubeconfig, err = e.WriteKubeconfig("mgmt", kubeconfig, port); err != nil {
			return err
		}
		cs, err := kube.Client(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "nodes Ready", Timeout: 3 * time.Minute, Interval: 2 * time.Second,
			Check: func(ctx context.Context) error { return kube.NodesReady(ctx, cs) },
		})
	}); err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return e.stage("kapp-controller", func() error { return platform.InstallKappController(ctx, c, e.infra) })
	})
	g.Go(func() error {
		return e.stage("cluster api", func() error { return platform.InstallClusterAPI(ctx, c, e.infra) })
	})
	return g.Wait()
}

const packageVersion = "0.1.0"

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

func (e *Environment) publishPackages(ctx context.Context, c *dagger.Client, reg *infra.Registry) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := build.ModuleRoot(wd)
	if err != nil {
		return err
	}
	refs := map[string]string{}
	for name, image := range build.Images(c, build.Source(c, root)) {
		if refs[name], err = reg.Push(ctx, image, name); err != nil {
			return fmt.Errorf("push %s: %w", name, err)
		}
	}
	config := build.Config(c, root)
	for _, p := range packages {
		images, err := subset(refs, p.images)
		if err != nil {
			return fmt.Errorf("package %s: %w", p.name, err)
		}
		lock, err := bundle.ImagesLock(images)
		if err != nil {
			return err
		}
		ref, err := reg.Push(ctx, bundle.Image(c, config.Directory(p.name), lock), "bundles/"+p.name)
		if err != nil {
			return fmt.Errorf("push bundle %s: %w", p.name, err)
		}
		pkg, err := bundle.Package(refName(p.name), packageVersion, ref)
		if err != nil {
			return err
		}
		e.Packages = append(e.Packages, pkg)
	}
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
	if err := platform.Apply(ctx, e.infra, bytes.Join(manifests, []byte("---\n"))); err != nil {
		return err
	}
	dyn, err := kube.Dynamic(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: "PackageInstalls reconciled", Timeout: 5 * time.Minute, Interval: 3 * time.Second,
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

// Verify checks the environment from the host through its kubeconfigs.
func (e *Environment) Verify(ctx context.Context) error {
	cs, err := kube.Client(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	dyn, err := kube.Dynamic(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	return errors.Join(
		kube.NodesReady(ctx, cs),
		kube.PackageInstallsReconciled(ctx, dyn, "devenv"),
		kube.ClusterAvailable(ctx, dyn, workloadNamespace, workloadCluster),
		kube.PackageInstallReconciled(ctx, dyn, workloadNamespace, remotePackageInstall),
	)
}

// Close ends the Dagger session, which stops every service in the environment.
func (e *Environment) Close() error {
	e.progress("tearing down")
	var errs []error
	for _, closer := range e.closers {
		errs = append(errs, closer())
	}
	return errors.Join(errs...)
}

// stage reports the start of a stage and names it in any error.
func (e *Environment) stage(name string, run func() error) error {
	e.progress(name)
	if err := run(); err != nil {
		return fmt.Errorf("stage %q: %w", name, err)
	}
	return nil
}

// ExportLogs writes cluster logs and resources to the environment directory, as far as bring-up got.
func (e *Environment) ExportLogs() {
	if e.infra == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = e.infra.ExportLogs(ctx, filepath.Join(e.Dir, "logs"))
}

func (e *Environment) progress(msg string) {
	if e.opts.Progress != nil {
		fmt.Fprintf(e.opts.Progress, "[%5.1fs] %s\n", time.Since(e.start).Seconds(), msg)
	}
}
