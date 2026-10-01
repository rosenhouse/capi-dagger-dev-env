// Package devenv brings up a development environment in one Dagger session.
package devenv

import (
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
		e.exportKindLogs()
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
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.managementCluster(ctx, c) })
	g.Go(func() error { return e.stage("images and bundles", func() error { return e.publishPackages(ctx, c) }) })
	return g.Wait()
}

func (e *Environment) managementCluster(ctx context.Context, c *dagger.Client) error {
	if err := e.stage("docker daemon", func() (err error) {
		e.infra, err = infra.Start(ctx, c, e.ID)
		return err
	}); err != nil {
		return err
	}
	return e.stage("management cluster", func() error {
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
	})
}

// packages lists the first-party bundles and the images each one locks.
var packages = []struct {
	name   string
	images []string
}{
	{"addon-manager", []string{"addon-manager"}},
	{"greeting-syncer", []string{"greeting-syncer"}},
	{"greeting-controller", []string{"greeting-controller", "hello"}},
}

func (e *Environment) publishPackages(ctx context.Context, c *dagger.Client) error {
	reg, err := infra.StartRegistry(ctx, c)
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := build.ModuleRoot(wd)
	if err != nil {
		return err
	}
	src := build.Source(c, root)
	refs := map[string]string{}
	for name, image := range build.Images(c, src) {
		if refs[name], err = reg.Push(ctx, image, name); err != nil {
			return fmt.Errorf("push %s: %w", name, err)
		}
	}
	for _, p := range packages {
		lock, err := bundle.ImagesLock(subset(refs, p.images))
		if err != nil {
			return err
		}
		ref, err := reg.Push(ctx, bundle.Image(c, src.Directory("config/"+p.name), lock), "bundles/"+p.name)
		if err != nil {
			return fmt.Errorf("push bundle %s: %w", p.name, err)
		}
		pkg, err := bundle.Package(p.name+".demo.example.com", "0.1.0", ref)
		if err != nil {
			return err
		}
		e.Packages = append(e.Packages, pkg)
	}
	return nil
}

func subset(m map[string]string, keys []string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

// Verify checks the environment from the host through its kubeconfigs.
func (e *Environment) Verify(ctx context.Context) error {
	cs, err := kube.Client(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	return kube.NodesReady(ctx, cs)
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

func (e *Environment) exportKindLogs() {
	if e.infra == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = e.infra.ExportLogs(ctx, filepath.Join(e.Dir, "kind-logs"))
}

func (e *Environment) progress(msg string) {
	if e.opts.Progress != nil {
		fmt.Fprintf(e.opts.Progress, "[%5.1fs] %s\n", time.Since(e.start).Seconds(), msg)
	}
}
