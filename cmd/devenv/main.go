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

	"github.com/spf13/cobra"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func main() {
	o := devenv.Options{Progress: os.Stderr, Command: command()}
	root := &cobra.Command{Use: "devenv", SilenceUsage: true}
	root.PersistentFlags().StringVar(&o.Name, "name", "", "environment name; up and test pick a random one if empty")
	root.PersistentFlags().StringVar(&o.StateDir, "state-dir", "", "directory for kubeconfigs and logs (default: .devenv in devenv's source)")
	root.PersistentFlags().BoolVarP(&o.Verbose, "verbose", "v", false, "stream guest command output to stderr")
	locate := func(*cobra.Command, []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		o.Source, err = devenv.Source(wd)
		if o.StateDir == "" {
			if err != nil {
				return err
			}
			o.StateDir = filepath.Join(o.Source, ".devenv")
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		o.CacheDir = filepath.Join(cache, "devenv")
		return nil
	}

	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Bring up an environment, which runs until down",
		Long:  "Bring up the environment called --name, or else the only environment, or else a new one with a random name.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := devenv.Up(cmd.Context(), o)
			if err != nil {
				return err
			}
			fmt.Printf("Environment %s is up.\n  management: export KUBECONFIG=%s\n  workload:   export KUBECONFIG=%s\n"+
				"After changing code, run: %s redeploy --name %s\nTo delete it, run: %s down --name %s\n",
				env.Name, env.MgmtKubeconfig, env.WorkloadKubeconfig, o.Command, env.Name, o.Command, env.Name)
			return env.Close()
		},
	}
	testCmd := &cobra.Command{
		Use:   "test",
		Short: "Bring up an environment, verify it, and delete it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return devenv.Test(cmd.Context(), o, os.Stdout)
		},
	}
	for _, cmd := range []*cobra.Command{upCmd, testCmd} {
		cmd.Flags().BoolVar(&o.Retain, "retain", false, "keep the VM if bring-up fails, for debugging")
		cmd.Flags().BoolVar(&o.Cold, "cold", false, "bring up the platform without its checkpoint")
		root.AddCommand(cmd)
	}
	platformCmd := &cobra.Command{Use: "platform", Short: "Manage the checkpoint that environments start from"}
	var inputs bool
	platformKeyCmd := &cobra.Command{
		Use:   "key",
		Short: "Print the key of this host's platform checkpoint",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			key, in, err := devenv.PlatformInputs()
			if err != nil {
				return err
			}
			if inputs {
				_, err = fmt.Printf("%s\n", in)
				return err
			}
			_, err = fmt.Println(key)
			return err
		},
	}
	platformKeyCmd.Flags().BoolVar(&inputs, "inputs", false, "print what the key hashes, as JSON")
	var force bool
	platformSaveCmd := &cobra.Command{
		Use:   "save",
		Short: "Bring up the platform in a new VM and save it as this host's platform checkpoint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := devenv.SavePlatform(cmd.Context(), o, force)
			if err != nil {
				return err
			}
			fmt.Printf("Saved %s.\n", path)
			return nil
		},
	}
	platformSaveCmd.Flags().BoolVar(&force, "force", false, "replace an existing checkpoint")
	platformSaveCmd.PreRunE = locate
	platformCmd.AddCommand(platformKeyCmd, platformSaveCmd)
	root.AddCommand(platformCmd)
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "List environments with the state of their VMs and their host ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return devenv.Status(cmd.Context(), o, os.Stdout)
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

	for _, cmd := range root.Commands() {
		cmd.PreRunE = locate
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	// After the first signal, a second one kills devenv.
	context.AfterFunc(ctx, stop)
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

// command returns how the user runs devenv. go run builds it in a temporary directory.
func command() string {
	if strings.Contains(os.Args[0], "go-build") {
		return "go run ./cmd/devenv"
	}
	return os.Args[0]
}
