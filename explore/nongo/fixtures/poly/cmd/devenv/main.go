// Command devenv runs a Go manager and a Dockerfile-built Python greeter.
package main

import (
	"log"
	"os"
	"strings"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
)

// hookRev is passed to the greeter build, so a test can tell which hook built it.
const hookRev = "1"

func main() {
	packages := []devenv.Package{
		{Name: "manager", RefName: "manager.poly.example.com", Config: "config/manager", Images: []string{"manager"}},
		{Name: "greeter", RefName: "greeter.poly.example.com", Config: "config/greeter", Images: []string{"greeter"}},
	}
	// POLY_WEB=1 adds a package that runs a patched nginx; POLY_WEB_IMAGES lists the images it locks.
	if os.Getenv("POLY_WEB") == "1" {
		packages = append(packages, devenv.Package{Name: "web", RefName: "web.poly.example.com", Config: "config/web",
			Images: strings.Fields(os.Getenv("POLY_WEB_IMAGES"))})
	}
	cli.Main(devenv.Config{Commands: []string{"./cmd/manager"}, Images: images, Packages: packages})
}

// images builds the Python greeter, and a patched nginx, from their Dockerfiles. Env vars, read by the up process, vary the build.
func images(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
	if _, err := os.Stat("PANIC_HOOK"); err == nil {
		panic("hook failed: PANIC_HOOK exists")
	}
	log.Printf("OBS-HOOK: images hook rev %s called with version=%s", hookRev, version)
	if os.Getenv("POLY_NIL_IMAGE") == "1" {
		return map[string]*dagger.Container{"greeter": nil}
	}
	opts := dagger.DirectoryDockerBuildOpts{Target: "app", BuildArgs: []dagger.BuildArg{{Name: "HOOK_REV", Value: hookRev}}}
	if os.Getenv("POLY_VERSION_ARG") != "0" {
		opts.BuildArgs = append(opts.BuildArgs, dagger.BuildArg{Name: "VERSION", Value: version})
	}
	return map[string]*dagger.Container{
		"greeter": src.Directory("services/greeter").DockerBuild(opts),
		"nginx":   src.Directory("services/nginx").DockerBuild(),
	}
}
