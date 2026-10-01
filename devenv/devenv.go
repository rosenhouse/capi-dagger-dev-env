// Package devenv brings up a development environment in one Dagger session.
package devenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dagger.io/dagger"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/build"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/bundle"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/control"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/state"
)

// Options names an environment and says where it reports.
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
	MgmtKubeconfig     string
	WorkloadKubeconfig string
	// packages are rendered Package resources for the bundles in the session registry.
	packages [][]byte
	// bundles maps each package name to its bundle's digest reference.
	bundles map[string]string

	cfg    Config
	opts   Options
	start  time.Time
	ctx    context.Context
	cancel context.CancelFunc
	// stopServing stops the control socket after its requests in flight.
	stopServing func() error
	isUp        atomic.Bool
	// redeploying serializes redeploys.
	redeploying sync.Mutex
	infra       *infra.Infra
	registry    *infra.Registry
	client      *dagger.Client
	mirrors     infra.Mirrors
	closers     []func() error
}

// Up brings up an environment for cfg's components and waits for its readiness gates.
func Up(ctx context.Context, cfg Config, o Options) (*Environment, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if cfg.Root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if cfg.Root, err = build.ModuleRoot(wd); err != nil {
			return nil, err
		}
	}
	e, err := start(ctx, o)
	if err != nil {
		return nil, err
	}
	e.cfg = cfg
	if err := e.bringUp(e.ctx); err != nil {
		e.ExportLogs()
		e.Close()
		return nil, fmt.Errorf("%w\nlogs: %s", err, e.Dir)
	}
	e.isUp.Store(true)
	return e, nil
}

// start locks the environment and serves its control socket.
func start(ctx context.Context, o Options) (*Environment, error) {
	env, err := state.New(o.StateDir, o.Name)
	if err != nil {
		return nil, err
	}
	unlock, err := env.Lock()
	if err != nil {
		return nil, err
	}
	stale, _ := filepath.Glob(filepath.Join(env.Dir, "*.kubeconfig"))
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			unlock()
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &Environment{Env: env, opts: o, start: time.Now(), ctx: ctx, cancel: cancel,
		closers: []func() error{func() error { cancel(); unlock(); return nil }}}
	listening, served := make(chan struct{}), make(chan error, 1)
	go func() {
		err := control.Serve(ctx, env.SocketPath(), e.handlers(), func() { close(listening) })
		if err != nil {
			cancel()
		}
		served <- err
	}()
	select {
	case <-listening:
	case err := <-served:
		e.Close()
		return nil, fmt.Errorf("control socket: %w", err)
	}
	e.stopServing = sync.OnceValue(func() error { cancel(); return <-served })
	return e, nil
}

// Context lasts until the environment is interrupted, asked to stop, or closed.
func (e *Environment) Context() context.Context { return e.ctx }

// validVersion matches versions that -ldflags can stamp, or none.
var validVersion = regexp.MustCompile(`^[A-Za-z0-9._+-]*$`)

func (e *Environment) handlers() map[string]control.Handler {
	return map[string]control.Handler{
		"down": func(context.Context, []string, io.Writer) error { e.cancel(); return nil },
		// redeploy takes an optional version to stamp the build with.
		"redeploy": func(ctx context.Context, args []string, progress io.Writer) error {
			return e.Redeploy(ctx, strings.Join(args, " "), progress)
		},
	}
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
	e.client = c

	if err := e.stage("preflight", func() error { return infra.Preflight(ctx, c) }); err != nil {
		return err
	}
	if err := e.stage("registry and mirrors", func() (err error) {
		e.registry, e.mirrors, err = infra.StartRegistries(ctx, c)
		return err
	}); err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.managementCluster(gctx, c) })
	g.Go(func() error {
		return e.stage("images and bundles", func() error { return e.publishPackages(gctx, "") })
	})
	if err := g.Wait(); err != nil {
		return err
	}
	if err := e.stage("management packages", func() error { return e.installPackages(ctx) }); err != nil {
		return err
	}
	return e.stage("workload cluster", func() error { return e.workloadCluster(ctx, c) })
}

// The workload Cluster's name and namespace in the management cluster.
const (
	WorkloadCluster   = "work"
	WorkloadNamespace = "default"
)

// workloadCluster creates the workload cluster, tunnels its API server, and waits for the consumer's components.
func (e *Environment) workloadCluster(ctx context.Context, c *dagger.Client) error {
	if err := platform.CreateWorkloadCluster(ctx, c, e.infra, WorkloadCluster, WorkloadNamespace); err != nil {
		return err
	}
	dyn, err := kube.Dynamic(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	if err := ready.Wait(ctx, ready.Gate{
		Name: "workload Cluster Available", Timeout: 10 * time.Minute, Interval: 5 * time.Second,
		Check: func(ctx context.Context) error {
			return kube.ClusterAvailable(ctx, dyn, WorkloadNamespace, WorkloadCluster)
		},
	}); err != nil {
		return err
	}
	if err := e.stage("workload API", func() error { return e.workloadAPI(ctx) }); err != nil {
		return err
	}
	if e.cfg.Ready == nil {
		return nil
	}
	return e.stage("consumer components", func() error { return e.cfg.Ready(ctx, e) })
}

// workloadAPI tunnels the workload API server to the host and writes its kubeconfig.
func (e *Environment) workloadAPI(ctx context.Context) error {
	if err := e.infra.ForwardWorkloadAPI(ctx, WorkloadCluster); err != nil {
		return err
	}
	port, err := e.infra.Tunnel(ctx, infra.WorkloadAPIPort)
	if err != nil {
		return err
	}
	mgmt, err := kube.Client(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	secret, err := mgmt.CoreV1().Secrets(WorkloadNamespace).Get(ctx, WorkloadCluster+"-kubeconfig", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if e.WorkloadKubeconfig, err = e.WriteKubeconfig("workload", secret.Data["value"], port); err != nil {
		return err
	}
	workload, err := kube.Client(e.WorkloadKubeconfig)
	if err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: "workload nodes Ready from host", Timeout: 2 * time.Minute, Interval: 2 * time.Second,
		Check: func(ctx context.Context) error { return kube.NodesReady(ctx, workload) },
	})
}

func (e *Environment) managementCluster(ctx context.Context, c *dagger.Client) error {
	if err := e.stage("docker daemon", func() (err error) {
		e.infra, err = infra.Start(ctx, c, e.ID, e.registry.Host, e.mirrors)
		if err == nil {
			e.closers = append([]func() error{e.infra.Close}, e.closers...)
		}
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

// packageVersion is the version of every Package. Redeploys update Packages in place.
const packageVersion = "0.1.0"

// publishPackages builds images and bundles from the current source, stamped with version,
// or with a digest of the source if version is empty.
func (e *Environment) publishPackages(ctx context.Context, version string) error {
	c, reg := e.client, e.registry
	spec := build.Spec{Root: e.cfg.Root, Commands: e.cfg.Commands}
	for _, p := range e.cfg.Packages {
		spec.ConfigDirs = append(spec.ConfigDirs, p.Config)
	}
	b, err := build.FromHost(ctx, c, spec, version)
	if err != nil {
		return err
	}
	var hook map[string]*dagger.Container
	if e.cfg.Images != nil {
		hook = e.cfg.Images(c, b.Source, b.Version)
	}
	images, err := e.cfg.images(build.Images(c, b.Binaries, e.cfg.Commands), hook)
	if err != nil {
		return err
	}
	refs := map[string]string{}
	for name, image := range images {
		if refs[name], err = reg.Push(ctx, image, name); err != nil {
			return fmt.Errorf("push %s: %w", name, err)
		}
	}
	var pkgs [][]byte
	bundles := map[string]string{}
	for _, p := range e.cfg.Packages {
		images, err := subset(refs, p.Images)
		if err != nil {
			return fmt.Errorf("package %s: %w", p.Name, err)
		}
		lock, err := bundle.ImagesLock(images)
		if err != nil {
			return err
		}
		ref, err := reg.Push(ctx, bundle.Image(c, b.Config.Directory(p.Config), lock), "bundles/"+p.Name)
		if err != nil {
			return fmt.Errorf("push bundle %s: %w", p.Name, err)
		}
		pkg, err := bundle.Package(p.RefName, packageVersion, ref)
		if err != nil {
			return err
		}
		pkgs = append(pkgs, pkg)
		bundles[p.Name] = ref
	}
	e.packages, e.bundles = pkgs, bundles
	return nil
}

const (
	installerNamespace      = "devenv"
	installerServiceAccount = "devenv-installer"
)

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
	pkgis, err := e.cfg.packageInstalls()
	if err != nil {
		return err
	}
	manifests := slices.Concat([][]byte{[]byte(installerManifests)}, e.packages, pkgis)
	if err := platform.Apply(ctx, e.infra, bytes.Join(manifests, []byte("---\n"))); err != nil {
		return err
	}
	dyn, err := kube.Dynamic(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: "PackageInstalls reconciled", Timeout: 5 * time.Minute, Interval: 3 * time.Second,
		Check: func(ctx context.Context) error { return kube.PackageInstallsReconciled(ctx, dyn, installerNamespace) },
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

// Redeploy rebuilds images and bundles from the current source and waits for every App,
// in any namespace, that fetches one of them to deploy the new bundle. An empty version names the build by its source.
// It reports its stages to progress as well as to the environment's own progress.
func (e *Environment) Redeploy(ctx context.Context, version string, progress io.Writer) error {
	if !e.isUp.Load() {
		return errors.New("the environment is still coming up")
	}
	if !e.redeploying.TryLock() {
		return errors.New("another redeploy is in progress")
	}
	defer e.redeploying.Unlock()
	if !validVersion.MatchString(version) {
		return fmt.Errorf("version %q is not letters, digits and ._+-", version)
	}
	stage := func(name string, run func() error) error {
		fmt.Fprintln(progress, name)
		return e.stage(name, run)
	}
	if err := stage("rebuild images and bundles", func() error { return e.publishPackages(ctx, version) }); err != nil {
		return err
	}
	return stage("redeploy packages", func() error {
		if err := platform.Reapply(ctx, e.infra, bytes.Join(e.packages, []byte("---\n"))); err != nil {
			return err
		}
		dyn, err := kube.Dynamic(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "Apps deployed their new bundles", Timeout: 5 * time.Minute, Interval: 2 * time.Second,
			Check: func(ctx context.Context) error {
				return kube.BundleAppsDeployed(ctx, dyn, e.cfg.bundlesOn(Management, e.bundles), e.cfg.bundlesOn(Workload, e.bundles))
			},
		})
	})
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
	workload, err := kube.Client(e.WorkloadKubeconfig)
	if err != nil {
		return err
	}
	catalogs := map[string]string{}
	for name, mirror := range e.mirrors {
		if catalogs[name], err = mirror.Catalog(ctx); err != nil {
			return err
		}
	}
	return errors.Join(
		missingMirroredRepos(catalogs, mirroredRepos),
		kube.NodesReady(ctx, cs),
		kube.NodesReady(ctx, workload),
		kube.PackageInstallsReconciled(ctx, dyn, "devenv"),
		kube.ClusterAvailable(ctx, dyn, WorkloadNamespace, WorkloadCluster),
	)
}

// mirroredRepos are repositories that bring-up pulls through each mirror. kindest/node arrives only
// through the Docker daemon's mirror; the others through containerd in the nodes.
var mirroredRepos = map[string][]string{
	"docker.io":       {"kindest/node", "kindest/kindnetd"},
	"registry.k8s.io": {"cluster-api/cluster-api-controller"},
	"ghcr.io":         {"carvel-dev/kapp-controller"},
	"quay.io":         {"jetstack/cert-manager-controller"},
	"gcr.io":          {"k8s-staging-cluster-api/capd-manager"},
}

// missingMirroredRepos names the repositories in want that the mirrors' catalogs lack.
func missingMirroredRepos(catalogs map[string]string, want map[string][]string) error {
	var missing []string
	for _, mirror := range slices.Sorted(maps.Keys(want)) {
		repos := strings.Fields(catalogs[mirror])
		for _, repo := range want[mirror] {
			if !slices.Contains(repos, repo) {
				missing = append(missing, mirror+"/"+repo)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("mirrors lack repositories: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Close ends the Dagger session, which stops every service in the environment.
func (e *Environment) Close() error {
	e.progress("tearing down")
	var errs []error
	if e.stopServing != nil {
		errs = append(errs, e.stopServing())
	}
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
	if e.MgmtKubeconfig != "" {
		// Checking from inside the session tells a failed API server from a failed host tunnel.
		readyz := "ready"
		if _, err := e.infra.Run(ctx, nil, "kubectl get --raw=/readyz --request-timeout=10s"); err != nil {
			readyz = err.Error()
		}
		e.progress("management API, from inside the session: " + readyz)
	}
	_ = e.infra.ExportLogs(ctx, filepath.Join(e.Dir, "logs"))
}

func (e *Environment) progress(msg string) {
	if e.opts.Progress != nil {
		fmt.Fprintf(e.opts.Progress, "[%5.1fs] %s\n", time.Since(e.start).Seconds(), msg)
	}
}
