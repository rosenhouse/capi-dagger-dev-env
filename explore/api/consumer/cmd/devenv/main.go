// Command devenv runs legacyctl in a Cluster API development environment.
package main

import (
	"os"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"

	"example.com/legacyctl/internal/scenario"
)

func main() { cli.Main(scenario.Config(os.Getenv("DEVENV_SCENARIO"))) }
