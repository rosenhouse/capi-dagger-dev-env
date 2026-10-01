// Package devenv runs each development environment in its own smolvm VM.
package devenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
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

// open holds env and opens its guest log.
func open(env state.Env, o Options) (*Environment, error) {
	unlock, err := env.Lock()
	if err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(env.Dir, "guest.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
	e, err := open(env, o)
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
	return os.Truncate(filepath.Join(e.Dir, "guest.log"), 0)
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

// The workload Cluster's name and namespace in the management cluster.
const (
	WorkloadCluster   = "work"
	WorkloadNamespace = "default"
	// remotePackageInstall is the name addon-manager gives greeting-controller's PackageInstall.
	remotePackageInstall = WorkloadCluster + "-greeting-controller"
)

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
	e, err := open(env, o)
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
