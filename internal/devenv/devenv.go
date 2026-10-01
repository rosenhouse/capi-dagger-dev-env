// Package devenv runs each development environment in its own smolvm VM.
package devenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/bundle"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/fetch"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/hostbuild"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/oci"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

type Options struct {
	Name     string
	StateDir string
	// Verbose streams guest command output to stderr as well as to the environment's guest.log.
	Verbose bool
	// Progress receives one line as each stage starts, and one as it ends.
	Progress io.Writer
	SmolVM   smolvm.CLI
}

// Environment is an environment that this process holds until Close.
type Environment struct {
	state.Env
	Ports              state.Ports
	MgmtKubeconfig     string
	WorkloadKubeconfig string
	// Packages are rendered Package resources for the first-party bundles in the session registry.
	Packages [][]byte
	// bundles maps each package name to its bundle's digest reference.
	bundles map[string]string

	opts     Options
	start    time.Time
	vm       *infra.VM
	cacheDir string
	closers  []func() error
}

// open holds env and opens its guest log, truncated if fresh.
func open(env state.Env, o Options, fresh bool) (*Environment, error) {
	unlock, err := env.Lock()
	if err != nil {
		return nil, err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fresh {
		flags |= os.O_TRUNC
	}
	logFile, err := os.OpenFile(filepath.Join(env.Dir, "guest.log"), flags, 0o600)
	if err != nil {
		unlock()
		return nil, err
	}
	var log io.Writer = logFile
	if o.Verbose {
		log = io.MultiWriter(logFile, os.Stderr)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return &Environment{
		Env:                env,
		MgmtKubeconfig:     filepath.Join(env.Dir, "mgmt.kubeconfig"),
		WorkloadKubeconfig: filepath.Join(env.Dir, "workload.kubeconfig"),
		opts:               o,
		start:              time.Now(),
		vm:                 &infra.VM{CLI: o.SmolVM, Name: env.VM(), Log: &syncWriter{w: log}},
		cacheDir:           filepath.Join(cacheDir, "devenv"),
		closers:            []func() error{logFile.Close, func() error { unlock(); return nil }},
	}, nil
}

// Up creates the environment called o.Name, or a randomly named one, and waits for its readiness gates.
// A failed bring-up exports logs and deletes the VM.
func Up(ctx context.Context, o Options) (*Environment, error) {
	env, err := state.New(o.StateDir, o.Name)
	if err != nil {
		return nil, err
	}
	e, err := open(env, o, true)
	if err != nil {
		return nil, err
	}
	var leftover smolvm.State
	if err := e.stage("preflight", func() (err error) { leftover, err = e.preflight(ctx); return err }); err != nil {
		return nil, errors.Join(err, e.Close())
	}
	if err := e.forget(); err != nil {
		return nil, errors.Join(err, e.Close())
	}
	if err := e.bringUp(ctx, leftover); err != nil {
		e.ExportLogs(ctx)
		_, deleteErr := e.Delete(ctx, false)
		return nil, errors.Join(fmt.Errorf("%w\nlogs: %s", err, e.Dir), deleteErr, e.Close())
	}
	return e, nil
}

// kvmDevice is a variable so that tests can stand in for KVM.
var kvmDevice = "/dev/kvm"

// preflight checks the host and returns the state of a VM left by an earlier run, which must not be running.
func (e *Environment) preflight(ctx context.Context) (smolvm.State, error) {
	if err := errors.Join(e.opts.SmolVM.CheckVersion(ctx), checkKVM(kvmDevice)); err != nil {
		return "", err
	}
	machines, err := e.opts.SmolVM.List(ctx)
	if err != nil {
		return "", err
	}
	if e.Running(machines) {
		return "", fmt.Errorf("environment %s is already up; use it, or delete it with: devenv down --name %s", e.Name, e.Name)
	}
	return e.VMState(machines), nil
}

// checkKVM fails on Linux unless this user can open device, KVM's, to read and write.
func checkKVM(device string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist: enable virtualization in the firmware, or nested virtualization on a cloud VM", device)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("cannot open %s: add your user to its group, for example with sudo usermod -aG kvm $USER, then log in again", device)
	case err != nil:
		return err
	}
	return f.Close()
}

// forget deletes what an earlier run left in the env dir.
func (e *Environment) forget() error {
	for _, path := range []string{e.MgmtKubeconfig, e.WorkloadKubeconfig, filepath.Join(e.Dir, "ports.json"), filepath.Join(e.Dir, "logs")} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// bringUp brings up the platform while it builds the first-party images, then installs them.
func (e *Environment) bringUp(ctx context.Context, leftover smolvm.State) error {
	var b build
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.platform(gctx, leftover) })
	g.Go(func() error {
		return e.stage("build images and bundles", func() (err error) { b, err = e.build(gctx, ""); return err })
	})
	if err := g.Wait(); err != nil {
		return err
	}
	return e.firstParty(ctx, b)
}

// apiAttempt bounds each check of a gate that calls an API server through a published port,
// because smolvm accepts a connection before anything in the guest answers it.
const apiAttempt = 30 * time.Second

// The workload Cluster's name and namespace in the management cluster.
const (
	WorkloadCluster   = "work"
	WorkloadNamespace = "default"
	// remotePackageInstall is the name addon-manager gives greeting-controller's PackageInstall.
	remotePackageInstall = WorkloadCluster + "-greeting-controller"
)

// platform brings up the VM and everything in it that holds no first-party code: dockerd, the session registry,
// the management cluster with kapp-controller, CAPI and CAPD, and the workload cluster.
func (e *Environment) platform(ctx context.Context, leftover smolvm.State) error {
	cache := fetch.Cache{Dir: filepath.Join(e.cacheDir, "downloads")}
	downloads, err := infra.Downloads(runtime.GOARCH)
	if err != nil {
		return err
	}
	downloads = append(downloads, platform.Downloads()...)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.stage("VM", func() error { return e.createVM(gctx, leftover) }) })
	g.Go(func() error {
		return e.stage("downloads", func() error {
			g, ctx := errgroup.WithContext(gctx)
			for _, d := range downloads {
				g.Go(func() error { _, err := cache.Get(ctx, d.File); return err })
			}
			return g.Wait()
		})
	})
	if err := g.Wait(); err != nil {
		return err
	}
	if err := e.stage("guest tools", func() error {
		if err := e.vm.Copy(ctx, cache, downloads); err != nil {
			return err
		}
		return e.vm.Install(ctx)
	}); err != nil {
		return err
	}
	if err := e.stage("docker daemon", func() error { return e.vm.StartDocker(ctx) }); err != nil {
		return err
	}
	if err := e.stage("registry", e.startRegistry(ctx)); err != nil {
		return err
	}
	if err := e.stage("management cluster", e.managementCluster(ctx)); err != nil {
		return err
	}
	g, gctx = errgroup.WithContext(ctx)
	g.Go(func() error {
		return e.stage("kapp-controller", func() error { return platform.InstallKappController(gctx, e.vm) })
	})
	g.Go(func() error {
		return e.stage("cluster api", func() error { return platform.InstallClusterAPI(gctx, e.vm) })
	})
	if err := g.Wait(); err != nil {
		return err
	}
	if err := e.stage("workload cluster", e.workloadCluster(ctx)); err != nil {
		return err
	}
	return e.stage("workload API", e.workloadAPI(ctx))
}

// createVM deletes a VM that an earlier run left, then creates the environment's VM on free host ports.
func (e *Environment) createVM(ctx context.Context, leftover smolvm.State) error {
	if leftover != "" {
		if err := e.vm.Delete(ctx); err != nil {
			return err
		}
	}
	ports, err := state.FreePorts()
	if err != nil {
		return err
	}
	if err := e.WritePorts(ports); err != nil {
		return err
	}
	e.Ports = ports
	return e.vm.Create(ctx, ports)
}

func (e *Environment) startRegistry(ctx context.Context) func() error {
	return func() error {
		if err := e.vm.StartRegistry(ctx); err != nil {
			return err
		}
		url := fmt.Sprintf("http://localhost:%d/v2/", e.Ports.Registry)
		return ready.Wait(ctx, ready.Gate{
			Name: "registry answers from host", Timeout: time.Minute, Interval: time.Second, Attempt: 5 * time.Second,
			Check: func(ctx context.Context) error { return get(ctx, url) },
		})
	}
}

func get(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return nil
}

func (e *Environment) managementCluster(ctx context.Context) func() error {
	return func() error {
		kubeconfig, err := e.vm.CreateManagementCluster(ctx)
		if err != nil {
			return err
		}
		if _, err := e.WriteKubeconfig("mgmt", kubeconfig, e.Ports.MgmtAPI); err != nil {
			return err
		}
		cs, err := kube.Client(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "nodes Ready", Timeout: 3 * time.Minute, Interval: 2 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error { return kube.NodesReady(ctx, cs) },
		})
	}
}

func (e *Environment) workloadCluster(ctx context.Context) func() error {
	return func() error {
		if err := platform.CreateWorkloadCluster(ctx, e.vm, WorkloadCluster, WorkloadNamespace); err != nil {
			return err
		}
		dyn, err := kube.Dynamic(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "workload Cluster Available", Timeout: 10 * time.Minute, Interval: 5 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error {
				return kube.ClusterAvailable(ctx, dyn, WorkloadNamespace, WorkloadCluster)
			},
		})
	}
}

// workloadAPI publishes the workload API server to the host and writes its kubeconfig.
func (e *Environment) workloadAPI(ctx context.Context) func() error {
	return func() error {
		if err := e.vm.ForwardWorkloadAPI(ctx, WorkloadCluster); err != nil {
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
		if _, err := e.WriteKubeconfig("workload", secret.Data["value"], e.Ports.WorkloadAPI); err != nil {
			return err
		}
		workload, err := kube.Client(e.WorkloadKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "workload nodes Ready from host", Timeout: 2 * time.Minute, Interval: 2 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error { return kube.NodesReady(ctx, workload) },
		})
	}
}

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

// Open holds the environment called o.Name, or the only running one. Its VM must be running.
func Open(ctx context.Context, o Options) (*Environment, error) {
	machines, err := o.SmolVM.List(ctx)
	if err != nil {
		return nil, err
	}
	env, err := state.Existing(o.StateDir, o.Name, machines)
	if err != nil {
		return nil, err
	}
	e, err := open(env, o, false)
	if err != nil {
		return nil, err
	}
	if machines, err = o.SmolVM.List(ctx); err == nil && !env.Running(machines) {
		err = fmt.Errorf("environment %s is not running", env.Name)
	}
	if err == nil {
		e.Ports, err = env.Ports()
	}
	if err != nil {
		return nil, errors.Join(err, e.Close())
	}
	return e, nil
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
	return errors.Join(
		kube.NodesReady(ctx, cs),
		kube.NodesReady(ctx, workload),
		kube.PackageInstallsReconciled(ctx, dyn, "devenv"),
		kube.ClusterAvailable(ctx, dyn, WorkloadNamespace, WorkloadCluster),
		kube.PackageInstallReconciled(ctx, dyn, WorkloadNamespace, remotePackageInstall),
	)
}

// Delete deletes the environment's VM, kubeconfigs and ports, and with purge its whole state dir.
// It reports whether there was a VM.
func (e *Environment) Delete(ctx context.Context, purge bool) (bool, error) {
	ctx = context.WithoutCancel(ctx)
	machines, err := e.opts.SmolVM.List(ctx)
	if err != nil {
		return false, err
	}
	existed := e.VMState(machines) != ""
	if existed {
		if err := e.vm.Delete(ctx); err != nil {
			return true, err
		}
	}
	paths := []string{e.MgmtKubeconfig, e.WorkloadKubeconfig, filepath.Join(e.Dir, "ports.json")}
	if purge {
		paths = []string{e.Dir}
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return existed, err
		}
	}
	return existed, nil
}

// Close releases the environment. Its VM keeps running.
func (e *Environment) Close() error {
	var errs []error
	for _, closer := range e.closers {
		errs = append(errs, closer())
	}
	return errors.Join(errs...)
}

// stage reports the start and duration of a stage and names it in any error.
func (e *Environment) stage(name string, run func() error) error {
	start := time.Now()
	e.progress(name)
	if err := run(); err != nil {
		return fmt.Errorf("stage %q: %w", name, err)
	}
	e.progress(fmt.Sprintf("%s: %.1fs", name, time.Since(start).Seconds()))
	return nil
}

// ExportLogs writes cluster logs and resources to the environment directory, as far as bring-up got.
func (e *Environment) ExportLogs(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	if machines, err := e.opts.SmolVM.List(ctx); err != nil || e.VMState(machines) == "" {
		return
	}
	if _, err := os.Stat(e.MgmtKubeconfig); err == nil {
		// Checking from inside the VM tells a failed API server from a failed published port.
		readyz := "ready"
		if err := e.vm.Run(ctx, "kubectl get --raw=/readyz --request-timeout=10s"); err != nil {
			readyz = err.Error()
		}
		e.progress("management API, from inside the VM: " + readyz)
	}
	if err := e.vm.ExportLogs(ctx, filepath.Join(e.Dir, "logs")); err != nil {
		e.progress("export logs: " + err.Error())
	}
}

func (e *Environment) progress(msg string) {
	if e.opts.Progress != nil {
		fmt.Fprintf(e.opts.Progress, "[%5.1fs] %s\n", time.Since(e.start).Seconds(), msg)
	}
}

// syncWriter serializes writes from guest commands that run at once.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
