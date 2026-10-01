package devenv

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// Down stops the environment's up process. With purge, it also deletes the environment's Docker data and state.
func Down(ctx context.Context, o Options, purge bool) error {
	env, err := state.Existing(o.StateDir, o.Name)
	if err != nil {
		return err
	}
	if err := stop(ctx, env, 2*time.Minute); err != nil {
		return err
	}
	if !purge {
		return nil
	}
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		return err
	}
	defer c.Close()
	if err := infra.Purge(ctx, c, env.ID); err != nil {
		return fmt.Errorf("purge Docker data: %w", err)
	}
	return os.RemoveAll(env.Dir)
}

// stop sends SIGTERM to the process holding env and waits for it to release the lock.
func stop(ctx context.Context, env state.Env, timeout time.Duration) error {
	pid, err := env.Holder()
	if err != nil || pid == 0 {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal up (pid %d): %w", pid, err)
	}
	return ready.Wait(ctx, ready.Gate{
		Name: fmt.Sprintf("environment %s stopped", env.Name), Timeout: timeout, Interval: 100 * time.Millisecond,
		Check: func(context.Context) error {
			if pid, err := env.Holder(); err != nil || pid != 0 {
				return fmt.Errorf("pid %d still running: %v", pid, err)
			}
			return nil
		},
	})
}
