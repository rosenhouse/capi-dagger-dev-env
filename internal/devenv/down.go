package devenv

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// Down deletes the VM of the environment called o.Name, even if its state dir is gone.
// Without a name, it picks the only environment, or else the only running one.
// With purge, it also deletes the environment's state dir.
func Down(ctx context.Context, o Options, purge bool, out io.Writer) error {
	machines, err := Machines(ctx, o)
	if err != nil {
		return err
	}
	env, err := state.Existing(o.StateDir, o.Name, machines)
	if err != nil && o.Name != "" {
		return deleteWithoutState(ctx, o, machines, out, err)
	}
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

// deleteWithoutState deletes the VM of the environment called o.Name, whose state dir is gone, or else returns notFound.
// Without its state dir, no other command can hold the environment.
func deleteWithoutState(ctx context.Context, o Options, machines []smolvm.Machine, out io.Writer, notFound error) error {
	env, err := state.New(o.StateDir, o.Name)
	if err != nil {
		return err
	}
	if env.VMState(machines) == "" {
		return notFound
	}
	if err := o.SmolVM.Delete(context.WithoutCancel(ctx), env.VM()); err != nil {
		return err
	}
	fmt.Fprintf(out, "Deleted the VM of environment %s.\n", env.Name)
	return nil
}
