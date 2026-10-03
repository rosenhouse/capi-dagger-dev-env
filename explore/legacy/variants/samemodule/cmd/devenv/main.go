// Command devenv runs fleet-addons in a Cluster API development environment.
package main

import (
	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
)

func main() {
	cli.Main(devenv.Config{
		Commands: []string{"./cmd", "./cmd/agent"},
		Packages: []devenv.Package{
			{Name: "fleet-addons", RefName: "fleet-addons.acme.io", Config: "config", Images: []string{"cmd", "agent"}},
		},
	})
}
