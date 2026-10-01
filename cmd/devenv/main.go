// Command devenv runs Cluster API development environments, each in its own smolvm VM.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/e2e"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func main() {
	o := devenv.Options{Progress: os.Stderr}
	root := &cobra.Command{Use: "devenv", SilenceUsage: true}
	root.PersistentFlags().StringVar(&o.Name, "name", "", "environment name; up and test pick a random one if empty")
	root.PersistentFlags().StringVar(&o.StateDir, "state-dir", ".devenv", "directory for kubeconfigs and logs")
	root.PersistentFlags().BoolVarP(&o.Verbose, "verbose", "v", false, "stream guest command output to stderr")

	root.AddCommand(&cobra.Command{
		Use:   "up",
		Short: "Bring up an environment, which runs until down",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := devenv.Up(cmd.Context(), o)
			if err != nil {
				return err
			}
			fmt.Printf("Environment %s is up.\n  management: export KUBECONFIG=%s\n  workload:   export KUBECONFIG=%s\n"+
				"After changing code, run: devenv redeploy --name %s\nTo delete it, run: devenv down --name %s\n",
				env.Name, env.MgmtKubeconfig, env.WorkloadKubeconfig, env.Name, env.Name)
			return env.Close()
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Bring up an environment, verify it, and delete it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			env, err := devenv.Up(ctx, o)
			if err != nil {
				return err
			}
			testErr := env.Verify(ctx)
			if testErr == nil {
				testErr = errors.Join(e2e.KubectlWorks(ctx, env.MgmtKubeconfig), e2e.KubectlWorks(ctx, env.WorkloadKubeconfig))
			}
			if testErr == nil {
				testErr = e2e.GreetingReachesWorkloadCluster(ctx, env.MgmtKubeconfig, env.WorkloadKubeconfig, devenv.WorkloadNamespace, devenv.WorkloadCluster)
			}
			if testErr == nil {
				testErr = env.Redeploy(ctx, "redeploy-test")
			}
			if testErr == nil {
				testErr = e2e.HelloServesVersion(ctx, env.WorkloadKubeconfig, "redeploy-test")
			}
			if testErr != nil {
				env.ExportLogs(ctx)
				testErr = fmt.Errorf("%w\nlogs: %s", testErr, env.Dir)
				if o.Name == "" {
					testErr = fmt.Errorf("%w\nclean up: devenv down --purge --name %s", testErr, env.Name)
				}
			}
			// Nothing reuses a random name's state once it passes.
			_, deleteErr := env.Delete(ctx, o.Name == "" && testErr == nil)
			if err := errors.Join(testErr, deleteErr, env.Close()); err != nil {
				return err
			}
			fmt.Printf("Environment %s passed.\n", env.Name)
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "List environments with the state of their VMs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			machines, err := devenv.Machines(cmd.Context(), o)
			if err != nil {
				return err
			}
			envs, err := state.List(o.StateDir)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tVM\tKUBECONFIGS")
			for _, env := range envs {
				vm, kubeconfigs := string(env.VMState(machines)), []string(nil)
				if vm == "" {
					vm = "none"
				}
				if env.Running(machines) {
					kubeconfigs, _ = filepath.Glob(filepath.Join(env.Dir, "*.kubeconfig"))
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", env.Name, vm, strings.Join(kubeconfigs, " "))
			}
			return w.Flush()
		},
	})
	var cluster string
	kubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Print the kubeconfig of a running environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			machines, err := devenv.Machines(cmd.Context(), o)
			if err != nil {
				return err
			}
			env, err := state.Existing(o.StateDir, o.Name, machines)
			if err != nil {
				return err
			}
			kubeconfig, err := env.Kubeconfig(cluster, machines)
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
			env, err := devenv.Open(cmd.Context(), o)
			if err != nil {
				return err
			}
			if err := errors.Join(env.Redeploy(cmd.Context(), version), env.Close()); err != nil {
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
		Short: "Delete an environment's VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return devenv.Down(cmd.Context(), o, purge, os.Stderr)
		},
	}
	downCmd.Flags().BoolVar(&purge, "purge", false, "also delete the environment's state dir")
	root.AddCommand(downCmd)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal, a second one kills devenv.
	context.AfterFunc(ctx, stop)
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
