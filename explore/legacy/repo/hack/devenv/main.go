// Command devenv runs fleet-addons in a Cluster API development environment.
// It is its own module, so the tool's dependencies stay out of fleet-addons' go.mod.
// DEVENV_SCENARIO picks one of the configurations an adopting team tried.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/build"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
)

const root = "../.."

func main() {
	scenario := os.Getenv("DEVENV_SCENARIO")
	cfg, ok := scenarios[scenario]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown DEVENV_SCENARIO %q\n", scenario)
		os.Exit(2)
	}
	cfg.Root = root
	cli.Main(cfg)
}

var scenarios = map[string]devenv.Config{
	// Step 0: only clusters. The tool needs a package, so this one holds a ConfigMap.
	"clusters-only": {
		Packages: []devenv.Package{{Name: "placeholder", RefName: "placeholder.acme.io", Config: "hack/devenv/placeholder"}},
	},
	// The kubebuilder config tree as is.
	"raw-config": {
		Commands: []string{"./cmd", "./cmd/agent"},
		Packages: []devenv.Package{{Name: "fleet-addons", RefName: "fleet-addons.acme.io", Config: "config", Images: []string{"cmd", "agent"}}},
	},
	// kustomize output rendered for the tool's image names.
	"rendered": {
		Commands: []string{"./cmd", "./cmd/agent"},
		Packages: []devenv.Package{{Name: "fleet-addons", RefName: "fleet-addons.acme.io", Config: "deploy/devenv", Images: []string{"cmd", "agent"}}},
		Images:   probeSource,
		Test:     ginkgo,
	},
	// kustomize output as make deploy renders it, with the team's Dockerfile building controller:latest.
	"dockerfile": {
		Packages: []devenv.Package{{Name: "fleet-addons", RefName: "fleet-addons.acme.io", Config: "deploy/legacy", Images: []string{"controller:latest", "agent:latest"}}},
		Images:   dockerfileImages,
		Test:     ginkgo,
	},
	// v3 layout: main.go at the module root.
	"root-main": {
		Commands: []string{"./"},
		Packages: []devenv.Package{{Name: "fleet-addons", RefName: "fleet-addons.acme.io", Config: "deploy/devenv", Images: []string{"fleet-addons"}}},
	},
	"root-main-dot": {
		Commands: []string{"."},
		Packages: []devenv.Package{{Name: "fleet-addons", RefName: "fleet-addons.acme.io", Config: "deploy/devenv", Images: []string{"fleet-addons"}}},
	},
	// Only the commands, to see build failures quickly.
	"build-only": {
		Commands: []string{"./cmd", "./cmd/agent"},
		Packages: []devenv.Package{{Name: "fleet-addons", RefName: "fleet-addons.acme.io", Config: "deploy/devenv", Images: []string{"cmd", "agent"}}},
	},
}

// probeSource reports what the build's source snapshot holds, then builds nothing extra.
func probeSource(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
	ctx := context.Background()
	state, err := src.Glob(ctx, "**/.devenv/**")
	fmt.Fprintf(os.Stderr, "OBS: Images hook: version=%s; source has %d .devenv entries %v (err %v)\n", version, len(state), head(state, 5), err)
	return nil
}

func head(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func dockerfileImages(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
	manager := src.DockerBuild(dagger.DirectoryDockerBuildOpts{
		BuildArgs: []dagger.BuildArg{{Name: "VERSION", Value: version}},
	})
	agent := build.Images(c, build.Binaries(c, src, version, []string{"./cmd/agent"}), []string{"./cmd/agent"})["agent"]
	return map[string]*dagger.Container{"controller:latest": manager, "agent:latest": agent}
}

// ginkgo runs the team's e2e suite against the environment.
func ginkgo(ctx context.Context, e *devenv.Environment) error {
	cmd := exec.CommandContext(ctx, "go", "test", "./test/e2e/", "-count=1", "-v", "-ginkgo.v")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MGMT_KUBECONFIG="+e.MgmtKubeconfig, "WORKLOAD_KUBECONFIG="+e.WorkloadKubeconfig)
	out, err := cmd.CombinedOutput()
	fmt.Fprintf(os.Stderr, "OBS: Test hook ran go test: err=%v\n%s\n", err, tail(string(out), 30))
	return err
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
