// Command devenv lives in its own module under tools/; the repository root has no go.mod.
package main

import (
	"context"
	"log"
	"os"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
)

func main() {
	root := "../.."
	if v, ok := os.LookupEnv("NESTED_ROOT"); ok {
		root = v
	}
	cli.Main(devenv.Config{
		Root:   root,
		Images: images,
		Packages: []devenv.Package{
			{Name: "greeter", RefName: "greeter.nested.example.com", Config: "config/greeter", Images: []string{"greeter"}},
		},
	})
}

func images(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
	state, err := src.Glob(context.Background(), "tools/devenv/.devenv/**")
	log.Printf("OBS-HOOK: version=%s; tools/devenv/.devenv entries in the source snapshot: %v (err %v)", version, state, err)
	return map[string]*dagger.Container{
		"greeter": src.Directory("services/greeter").DockerBuild(dagger.DirectoryDockerBuildOpts{
			BuildArgs: []dagger.BuildArg{{Name: "VERSION", Value: version}},
		}),
	}
}
