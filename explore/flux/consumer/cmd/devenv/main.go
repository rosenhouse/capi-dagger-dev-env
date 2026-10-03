// Command devenv is a synthetic Flux-based consumer. FLUXCO_SCENARIO picks its config.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
)

// artifacts are Flux sources built from the source snapshot as plain container images.
func artifacts(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
	return map[string]*dagger.Container{
		"agent-manifests":   c.Container().WithRootfs(c.Directory().WithDirectory(".", src.Directory("deploy/agent"))),
		"manager-manifests": c.Container().WithRootfs(c.Directory().WithDirectory(".", src.Directory("deploy/manager"))),
		"agent-chart":       c.Container().WithRootfs(c.Directory().WithDirectory("agent", src.Directory("chart/agent"))),
		// rootfs-probe shows what a subdirectory of the snapshot holds as a rootfs.
		"rootfs-probe": c.Container().WithRootfs(src.Directory("deploy/agent")),
	}
}

// script runs the script named by env with the environment's kubeconfigs.
func script(env string) func(ctx context.Context, e *devenv.Environment) error {
	return func(ctx context.Context, e *devenv.Environment) error {
		path := os.Getenv(env)
		if path == "" {
			return nil
		}
		cmd := exec.CommandContext(ctx, "bash", path)
		cmd.Env = append(os.Environ(), "MGMT="+e.MgmtKubeconfig, "WORK="+e.WorkloadKubeconfig)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
}

func config() devenv.Config {
	cfg := devenv.Config{
		Commands: []string{"./cmd/agent", "./cmd/manager"},
		Images:   artifacts,
		Ready:    script("FLUXCO_READY"),
		Test:     script("FLUXCO_TEST"),
	}
	refs := devenv.Package{Name: "refs", RefName: "refs.fluxco.example.com", Config: "config/refs",
		Images: []string{"agent", "manager", "agent-manifests", "manager-manifests", "agent-chart"}}
	switch s := os.Getenv("FLUXCO_SCENARIO"); s {
	case "nopkg":
	case "hybrid":
		cfg.Packages = []devenv.Package{
			{Name: "flux", RefName: "flux.fluxco.example.com", Config: "config/flux"},
			refs,
		}
	case "workloadonly":
		cfg.Packages = []devenv.Package{{Name: "dummy", RefName: "dummy.fluxco.example.com", Config: "config/dummy", On: devenv.Workload}}
	case "fluxonly":
		cfg.Packages = []devenv.Package{{Name: "dummy", RefName: "dummy.fluxco.example.com", Config: "config/dummy"}}
	case "kustomize":
		cfg.Packages = []devenv.Package{{Name: "manager", RefName: "manager.fluxco.example.com", Config: "config/kustomize", Images: []string{"manager"}}}
	case "refs":
		cfg.Packages = []devenv.Package{refs}
	default:
		fmt.Fprintf(os.Stderr, "unknown FLUXCO_SCENARIO %q\n", s)
		os.Exit(2)
	}
	return cfg
}

func main() { cli.Main(config()) }
