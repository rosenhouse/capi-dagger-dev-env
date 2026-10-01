package devenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/control"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/state"
)

// Down asks the environment's up process to stop and waits for it to exit.
func Down(ctx context.Context, env state.Env, out io.Writer) error {
	running, err := env.Running()
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintf(out, "Environment %s is not running.\n", env.Name)
		return nil
	}
	fmt.Fprintf(out, "Stopping environment %s.\n", env.Name)
	if err := control.Request(ctx, env.SocketPath(), "down", out); err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: fmt.Sprintf("environment %s stopped", env.Name), Timeout: 5 * time.Minute, Interval: 100 * time.Millisecond,
		Check: func(context.Context) error {
			if running, err := env.Running(); err != nil || running {
				return errors.Join(errors.New("up still holds the environment's lock"), err)
			}
			return nil
		},
	})
}

// Purge deletes a stopped environment's Docker data, state and socket. It holds the environment while it does.
func Purge(ctx context.Context, env state.Env) error {
	unlock, err := env.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		return err
	}
	defer c.Close()
	if err := infra.Purge(ctx, c, env.ID); err != nil {
		return fmt.Errorf("purge Docker data: %w", err)
	}
	if err := os.Remove(env.SocketPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(env.Dir)
}
