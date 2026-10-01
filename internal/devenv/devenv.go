// Package devenv brings up a development environment in one Dagger session.
package devenv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"dagger.io/dagger"

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

	opts    Options
	start   time.Time
	logPath string
	infra   *infra.Infra
	closers []func() error
}

// Up brings up an environment and waits for its readiness gates.
func Up(ctx context.Context, o Options) (*Environment, error) {
	env, err := state.New(o.StateDir, o.Name)
	if err != nil {
		return nil, err
	}
	e := &Environment{Env: env, opts: o, start: time.Now(), logPath: filepath.Join(env.Dir, "dagger.log")}
	if err := e.bringUp(ctx); err != nil {
		e.exportKindLogs()
		e.Close()
		return nil, fmt.Errorf("%w\nlogs: %s", err, env.Dir)
	}
	return e, nil
}

func (e *Environment) bringUp(ctx context.Context) error {
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return err
	}
	logFile, err := os.Create(e.logPath)
	if err != nil {
		return err
	}
	e.closers = append(e.closers, logFile.Close)
	var logs io.Writer = logFile
	if e.opts.Verbose {
		logs = io.MultiWriter(logFile, os.Stderr)
	}

	e.progress("connecting to Dagger engine")
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(logs), dagger.WithEnvironmentVariable("DAGGER_PROGRESS", "plain"))
	if err != nil {
		return fmt.Errorf("stage %q: %w", "connect", err)
	}
	e.closers = append([]func() error{c.Close}, e.closers...)

	e.progress("starting Docker daemon")
	if e.infra, err = infra.Start(ctx, c, e.Name); err != nil {
		return fmt.Errorf("stage %q: %w", "docker daemon", err)
	}

	e.progress("creating management cluster")
	kubeconfig, err := e.infra.CreateManagementCluster(ctx)
	if err != nil {
		return fmt.Errorf("stage %q: %w", "management cluster", err)
	}
	port, err := e.infra.Tunnel(ctx, infra.MgmtAPIPort)
	if err != nil {
		return fmt.Errorf("stage %q: tunnel: %w", "management cluster", err)
	}
	if e.MgmtKubeconfig, err = e.WriteKubeconfig("mgmt", kubeconfig, port); err != nil {
		return err
	}
	cs, err := kube.Client(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	return ready.Wait(ctx, "management cluster", ready.Gate{
		Name: "nodes Ready", Timeout: 3 * time.Minute, Interval: 2 * time.Second,
		Check: func(ctx context.Context) error { return kube.NodesReady(ctx, cs) },
	})
}

// Close ends the Dagger session, which stops every service in the environment.
func (e *Environment) Close() {
	for _, close := range e.closers {
		_ = close()
	}
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
