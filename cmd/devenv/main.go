// Command devenv runs a Cluster API development environment in a Dagger session.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv"
)

func main() {
	o := devenv.Options{Progress: os.Stderr}
	root := &cobra.Command{Use: "devenv", SilenceUsage: true}
	root.PersistentFlags().StringVar(&o.Name, "name", "", "environment name (default: random); reusing a name reuses its cached images")
	root.PersistentFlags().StringVar(&o.StateDir, "state-dir", ".devenv", "directory for kubeconfigs and logs")
	root.PersistentFlags().BoolVarP(&o.Verbose, "verbose", "v", false, "stream Dagger logs to stderr")

	root.AddCommand(&cobra.Command{
		Use:   "up",
		Short: "Bring up an environment and hold it until interrupted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := devenv.Up(cmd.Context(), o)
			if err != nil {
				return err
			}
			fmt.Printf("Environment %s is up.\n  export KUBECONFIG=%s\nPress Ctrl-C to tear it down.\n", env.Name, env.MgmtKubeconfig)
			<-cmd.Context().Done()
			return env.Close()
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Bring up an environment, verify it, and tear it down",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := devenv.Up(cmd.Context(), o)
			if err != nil {
				return err
			}
			verifyErr := env.Verify(cmd.Context())
			if err := errors.Join(verifyErr, env.Close()); err != nil {
				return err
			}
			fmt.Printf("Environment %s passed.\n", env.Name)
			return nil
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
