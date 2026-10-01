// Command devenv runs a Cluster API development environment in a Dagger session.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/control"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/e2e"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func main() {
	o := devenv.Options{Progress: os.Stderr}
	root := &cobra.Command{Use: "devenv", SilenceUsage: true}
	root.PersistentFlags().StringVar(&o.Name, "name", "", "environment name; up and test pick a random one if empty, and reusing a name reuses its cached images")
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
			fmt.Printf("Environment %s is up.\n  management: export KUBECONFIG=%s\n  workload:   export KUBECONFIG=%s\n"+
				"After changing code, run: devenv redeploy --name %s\nPress Ctrl-C to tear it down.\n",
				env.Name, env.MgmtKubeconfig, env.WorkloadKubeconfig, env.Name)
			<-env.Context().Done()
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
			testErr := env.Verify(env.Context())
			if testErr == nil {
				testErr = errors.Join(e2e.KubectlWorks(env.Context(), env.MgmtKubeconfig), e2e.KubectlWorks(env.Context(), env.WorkloadKubeconfig))
			}
			if testErr == nil {
				testErr = e2e.GreetingReachesWorkloadCluster(env.Context(), env.MgmtKubeconfig, env.WorkloadKubeconfig, devenv.WorkloadNamespace, devenv.WorkloadCluster)
			}
			if testErr == nil {
				testErr = control.Request(env.Context(), env.SocketPath(), "redeploy redeploy-test", io.Discard)
			}
			if testErr == nil {
				testErr = e2e.HelloServesVersion(env.Context(), env.WorkloadKubeconfig, "redeploy-test")
			}
			if testErr != nil {
				env.ExportLogs()
				testErr = fmt.Errorf("%w\nlogs: %s", testErr, env.Dir)
			}
			if err := errors.Join(testErr, env.Close()); err != nil {
				return err
			}
			fmt.Printf("Environment %s passed.\n", env.Name)
			if o.Name == "" {
				// Nothing could reuse a random name's cached Docker data.
				return devenv.Purge(cmd.Context(), env.Env)
			}
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "List environments and whether each is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			envs, err := state.List(o.StateDir)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATE\tKUBECONFIGS")
			for _, env := range envs {
				running, err := env.Running()
				status, kubeconfigs := "stopped", []string(nil)
				if err != nil {
					status = "unknown: " + err.Error()
				} else if running {
					status = "running"
					kubeconfigs, _ = filepath.Glob(filepath.Join(env.Dir, "*.kubeconfig"))
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", env.Name, status, strings.Join(kubeconfigs, " "))
			}
			return w.Flush()
		},
	})
	var cluster string
	kubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Print an environment's kubeconfig",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := state.Existing(o.StateDir, o.Name)
			if err != nil {
				return err
			}
			kubeconfig, err := env.Kubeconfig(cluster)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(kubeconfig)
			return err
		},
	}
	kubeconfigCmd.Flags().StringVar(&cluster, "cluster", "mgmt", "mgmt or workload")
	root.AddCommand(kubeconfigCmd)
	var version string
	redeployCmd := &cobra.Command{
		Use:   "redeploy",
		Short: "Rebuild from the current source and redeploy into a running environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := state.Existing(o.StateDir, o.Name)
			if err != nil {
				return err
			}
			if running, err := env.Running(); err != nil || !running {
				return errors.Join(fmt.Errorf("environment %s is not running", env.Name), err)
			}
			if err := control.Request(cmd.Context(), env.SocketPath(), strings.TrimSpace("redeploy "+version), os.Stderr); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Redeployed environment %s.\n", env.Name)
			return nil
		},
	}
	redeployCmd.Flags().StringVar(&version, "version", "", "version to stamp the build with (default: a digest of the source)")
	root.AddCommand(redeployCmd)
	var purge bool
	downCmd := &cobra.Command{
		Use:   "down",
		Short: "Stop a running environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := state.Existing(o.StateDir, o.Name)
			if err != nil {
				return err
			}
			if err := devenv.Down(cmd.Context(), env, os.Stderr); err != nil || !purge {
				return err
			}
			return devenv.Purge(cmd.Context(), env)
		},
	}
	downCmd.Flags().BoolVar(&purge, "purge", false, "also delete the environment's cached Docker data and state")
	root.AddCommand(downCmd)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal, a second one kills devenv.
	context.AfterFunc(ctx, stop)
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
