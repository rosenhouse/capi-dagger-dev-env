// Command mytool is an existing project CLI that embeds devenv as a subcommand.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"

	"example.com/legacyctl/internal/scenario"
)

func main() {
	root := &cobra.Command{Use: "mytool", SilenceUsage: true}
	dev := cli.Command(scenario.Config(os.Getenv("DEVENV_SCENARIO")))
	dev.Use = "devenv"
	root.AddCommand(dev)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
