package devenv

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// Down deletes the VM of the environment called o.Name, or of the only one, or else the only running one.
// With purge, it also deletes the environment's state dir.
func Down(ctx context.Context, o Options, purge bool, out io.Writer) error {
	machines, err := Machines(ctx, o)
	if err != nil {
		return err
	}
	env, err := state.Existing(o.StateDir, o.Name, machines)
	if err != nil {
		return err
	}
	e, err := open(env, o)
	if err != nil {
		return err
	}
	existed, err := e.Delete(ctx, purge)
	if existed {
		fmt.Fprintf(out, "Deleted the VM of environment %s.\n", env.Name)
	} else {
		fmt.Fprintf(out, "Environment %s has no VM.\n", env.Name)
	}
	if purge && err == nil {
		fmt.Fprintf(out, "Deleted %s.\n", env.Dir)
	}
	return errors.Join(err, e.Close())
}
