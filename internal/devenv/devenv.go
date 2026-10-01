// Package devenv runs each development environment in its own smolvm VM.
package devenv

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

type Options struct {
	Name     string
	StateDir string
	// Source is the root of devenv's own module, which it builds from.
	Source string
	// CacheDir holds downloads and base images for every environment.
	CacheDir string
	// Command is how the user runs devenv, for hints.
	Command string
	// Verbose streams guest command output to stderr as well as to the environment's guest.log.
	Verbose bool
	// Retain keeps the VM of a failed bring-up, for debugging.
	Retain bool
	// Cold brings up the platform without its checkpoint.
	Cold bool
	// Progress receives one line as each stage starts, and one as it ends.
	Progress io.Writer
	SmolVM   smolvm.CLI
}

// hint returns a devenv command line as the user runs devenv.
func (o Options) hint(args string) string { return cmp.Or(o.Command, "devenv") + " " + args }

// Environment is an environment that this process holds until Close.
type Environment struct {
	state.Env
	Ports              state.Ports
	MgmtKubeconfig     string
	WorkloadKubeconfig string
	// packageManifests are rendered Package resources for the first-party bundles in the environment's registry.
	packageManifests [][]byte
	// bundles maps each package name to its bundle's digest reference.
	bundles map[string]string

	opts  Options
	start time.Time
	vm    *infra.VM
	// branchable starts a new VM so that it can be checkpointed.
	branchable bool
	closers    []func() error
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
	return &Environment{
		Env:                env,
		MgmtKubeconfig:     filepath.Join(env.Dir, "mgmt.kubeconfig"),
		WorkloadKubeconfig: filepath.Join(env.Dir, "workload.kubeconfig"),
		opts:               o,
		start:              time.Now(),
		vm:                 &infra.VM{CLI: o.SmolVM, Name: env.VM(), Log: &syncWriter{w: log}},
		closers:            []func() error{logFile.Close, func() error { unlock(); return nil }},
	}, nil
}

// Up brings up the environment called o.Name, or else the only one, or else a new one with a random name,
// and waits for its readiness gates. A failed bring-up exports logs and deletes the VM, unless o.Retain.
func Up(ctx context.Context, o Options) (*Environment, error) {
	if err := checkHost(ctx, o); err != nil {
		return nil, err
	}
	env, err := upTarget(o)
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
	err = bringUp(e, ctx, leftover)
	if err == nil {
		err = e.MarkReady()
	}
	if err != nil {
		return nil, errors.Join(e.fail(ctx, err), e.Close())
	}
	return e, nil
}

// bringUp is a variable so that tests can stand in for VMs.
var bringUp = (*Environment).bringUp

// checkHost fails unless devenv has its source, smolvm's pinned version and KVM.
func checkHost(ctx context.Context, o Options) error {
	if o.Source == "" {
		return errNoSource
	}
	return checkVM(ctx, o)
}

// checkVM fails unless the host has smolvm's pinned version and KVM.
func checkVM(ctx context.Context, o Options) error {
	return errors.Join(o.SmolVM.CheckVersion(ctx), checkKVM(kvmDevice))
}

// upTarget returns the environment called o.Name, or else the only one, or else a new one with a random name.
func upTarget(o Options) (state.Env, error) {
	if o.Name == "" {
		envs, err := state.List(o.StateDir)
		if err != nil {
			return state.Env{}, err
		}
		if len(envs) == 1 {
			return envs[0], nil
		}
	}
	return state.New(o.StateDir, o.Name)
}

// kvmDevice is a variable so that tests can stand in for KVM.
var kvmDevice = "/dev/kvm"

// preflight returns the state of a VM that an earlier run left. A running VM must not have become ready.
func (e *Environment) preflight(ctx context.Context) (smolvm.State, error) {
	machines, err := e.opts.SmolVM.List(ctx)
	if err != nil {
		return "", err
	}
	if e.Running(machines) && e.Ready() {
		return "", fmt.Errorf("environment %s is already up; use it, or delete it with: %s", e.Name, e.opts.hint("down --name "+e.Name))
	}
	envs, err := state.List(e.opts.StateDir)
	if err != nil {
		return "", err
	}
	var others []string
	for _, env := range envs {
		if env.Name != e.Name && env.Running(machines) {
			others = append(others, env.Name)
		}
	}
	if len(others) > 0 {
		e.progress("also running here, each in its own VM: " + strings.Join(others, ", "))
	}
	return e.VMState(machines), nil
}

// checkKVM fails on Linux unless this user can open the KVM device to read and write.
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

// lastRun lists what a run leaves in the env dir, besides guest.log.
func (e *Environment) lastRun() []string {
	return []string{e.MgmtKubeconfig, e.WorkloadKubeconfig, filepath.Join(e.Dir, "ports.json"), filepath.Join(e.Dir, "ready")}
}

// forget deletes what an earlier run left in the env dir.
func (e *Environment) forget() error {
	for _, path := range append(e.lastRun(), filepath.Join(e.Dir, "logs")) {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return os.Truncate(filepath.Join(e.Dir, "guest.log"), 0)
}

// bringUp brings up the platform while it builds the first-party images, then installs them.
// The build waits for the VM to start, because both use every CPU, and smolvm gives a VM only 30 s to answer.
func (e *Environment) bringUp(ctx context.Context, leftover smolvm.State) error {
	started := make(chan struct{})
	var a artifacts
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.platform(gctx, leftover, sync.OnceFunc(func() { close(started) })) })
	g.Go(func() error {
		select {
		case <-started:
		case <-gctx.Done():
			return gctx.Err()
		}
		return e.stage("build images", func() (err error) { a, err = e.build(gctx, ""); return err })
	})
	if err := g.Wait(); err != nil {
		return err
	}
	return e.firstParty(ctx, a)
}

// fail exports logs, unless ctx has ended, and deletes the VM, unless o.Retain.
func (e *Environment) fail(ctx context.Context, err error) error {
	interrupted := ctx.Err() != nil
	if interrupted {
		err = errors.New("interrupted")
	} else {
		e.exportLogs(ctx)
		err = fmt.Errorf("%w\nlogs: %s", err, e.Dir)
	}
	if e.opts.Retain {
		return fmt.Errorf("%w\nkept the VM; delete it with: %s", err, e.opts.hint("down --name "+e.Name))
	}
	if _, deleteErr := e.Delete(ctx, false); deleteErr != nil {
		return errors.Join(err, deleteErr)
	}
	if interrupted {
		return errors.New("interrupted; deleted the VM")
	}
	return err
}

// The workload Cluster's name and namespace in the management cluster.
const (
	WorkloadCluster   = "work"
	WorkloadNamespace = "default"
	// remotePackageInstall is the name addon-manager gives greeting-controller's PackageInstall.
	remotePackageInstall = WorkloadCluster + "-greeting-controller"
)

// Machines lists smolvm's machines once it has checked smolvm's version.
func Machines(ctx context.Context, o Options) ([]smolvm.Machine, error) {
	if err := o.SmolVM.CheckVersion(ctx); err != nil {
		return nil, err
	}
	return o.SmolVM.List(ctx)
}

// Open holds the environment called o.Name, or the only one, or else the only running one. It must be up.
func Open(ctx context.Context, o Options) (*Environment, error) {
	if o.Source == "" {
		return nil, errNoSource
	}
	machines, err := Machines(ctx, o)
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
	if machines, err = o.SmolVM.List(ctx); err == nil {
		switch {
		case !env.Running(machines):
			err = fmt.Errorf("environment %s is not running", env.Name)
		case !env.Ready():
			err = fmt.Errorf("environment %s never became ready; bring it up again with: %s", env.Name, o.hint("up --name "+env.Name))
		}
	}
	if err == nil {
		e.Ports, err = env.ReadPorts()
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

// Delete deletes the environment's VM and what its last run left, and with purge its whole state dir.
// It finishes even if ctx ends. It reports whether there was a VM.
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
	paths := e.lastRun()
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

// wait waits for g, and logs its checks to guest.log.
func (e *Environment) wait(ctx context.Context, g ready.Gate) error {
	g.Log = e.vm.Log
	return ready.Wait(ctx, g)
}

// exportLogs writes cluster logs and resources to the environment directory, as far as bring-up got.
func (e *Environment) exportLogs(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
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
	if err := e.vm.ExportLogs(ctx, filepath.Join(e.Dir, "logs"), WorkloadCluster, WorkloadNamespace); err != nil {
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
