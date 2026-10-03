// Command devenv runs a Go manager and a Dockerfile-built Python greeter.
package main

import (
	"log"
	"os"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
)

func main() {
	cli.Main(devenv.Config{
		Commands: []string{"./cmd/manager"},
		Images:   images,
		Packages: []devenv.Package{
			{Name: "manager", RefName: "manager.poly.example.com", Config: "config/manager", Images: []string{"manager"}},
			{Name: "greeter", RefName: "greeter.poly.example.com", Config: "config/greeter", Images: []string{"greeter"}},
		},
	})
}

// images builds the Python greeter from its Dockerfile. Env vars, read by the up process, vary the build.
func images(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
	if _, err := os.Stat("PANIC_HOOK"); err == nil {
		panic("hook failed: PANIC_HOOK exists")
	}
	log.Printf("OBS-HOOK: images hook called with version=%s", version)
	opts := dagger.DirectoryDockerBuildOpts{Target: "app"}
	if os.Getenv("POLY_VERSION_ARG") != "0" {
		opts.BuildArgs = []dagger.BuildArg{{Name: "VERSION", Value: version}}
	}
	return map[string]*dagger.Container{
		"greeter": src.Directory("services/greeter").DockerBuild(opts),
	}
}
